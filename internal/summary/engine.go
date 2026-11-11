package summary

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/google/go-github/v88/github"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
)

type GitHubSource interface {
	RepositoryByID(context.Context, int64, gh.Policy) (gh.RepositoryResult, error)
	Readme(context.Context, string, string, string, int, gh.Policy) (gh.ReadmeResult, error)
}

type Engine struct {
	Queue     Queue
	GitHub    GitHubSource
	Provider  Provider
	Translate func(context.Context, string) ModelResult
	Logger    *slog.Logger
	Tick      time.Duration
}

func (e *Engine) Run(ctx context.Context) error {
	for {
		ready, err := storage.Initialized(ctx, e.Queue.DB)
		if ready && err == nil {
			break
		}
		if err != nil && !storage.IsBusy(err) {
			return err
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
	group, workerCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		for workerCtx.Err() == nil {
			now := time.Now()
			err := e.Queue.Recover(workerCtx, now)
			if err == nil && e.Provider != nil && e.GitHub != nil {
				err = e.Queue.Schedule(workerCtx, now)
			}
			if err != nil && workerCtx.Err() == nil {
				e.Logger.Warn("摘要调度失败", "error", err)
			}
			if err := wait(workerCtx, e.Tick); err != nil {
				return err
			}
		}
		return workerCtx.Err()
	})
	if e.Provider != nil && e.GitHub != nil {
		for range e.Queue.AI.Summary.Workers {
			group.Go(func() error {
				for workerCtx.Err() == nil {
					done, err := e.Step(workerCtx)
					if err != nil && !errors.Is(err, errLease) && workerCtx.Err() == nil {
						// 模型及 GitHub 错误已转为固定码，日志只接收数据库等本地错误。
						e.Logger.Warn("摘要 Worker 未完成", "error", err)
					}
					if !done || err != nil {
						if err := wait(workerCtx, time.Second); err != nil {
							return err
						}
					}
				}
				return workerCtx.Err()
			})
		}
	}
	return group.Wait()
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e *Engine) Step(ctx context.Context) (bool, error) {
	job, err := e.Queue.claim(ctx, time.Now())
	if err != nil || job == nil {
		return false, err
	}
	taskCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Duration(e.Queue.LeaseSeconds) * time.Second / 3)
		defer t.Stop()
		for {
			select {
			case <-taskCtx.Done():
				return
			case now := <-t.C:
				if err := checked(fence(e.Queue.DB.WithContext(taskCtx), *job, now.UnixMilli()).
					Update("lease_until", now.Add(time.Duration(e.Queue.LeaseSeconds)*time.Second).UnixMilli())); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = e.runJob(taskCtx, *job)
	cancel()
	<-done
	if final, release, draining := lifecycle.Settlement(ctx); draining {
		defer release()
		if err != nil {
			lifecycle.RecordSettlement(ctx, storage.InterruptJob(final, e.Queue.DB, *job))
		}
	}
	return true, err
}

func (e *Engine) runJob(ctx context.Context, job storage.Job) error {
	q := e.Queue
	var p payload
	if json.Unmarshal([]byte(job.PayloadJSON), &p) != nil {
		return e.fail(ctx, job, "summary_invalid_job", false, 0, false)
	}
	var repo storage.Repository
	if err := q.DB.WithContext(ctx).Take(&repo, "repo_id=?", *job.RepoID).Error; err != nil {
		return err
	}
	if repo.Blocked || repo.Disabled || !repo.NameIsCurrent || repo.State == "unavailable" {
		return e.fail(ctx, job, "summary_repo_unavailable", false, 0, false)
	}
	if job.Type == TranslationJob {
		return e.runTranslation(ctx, job, repo, p.Manual)
	}
	var old storage.AISummary
	var check storage.SummaryCheck
	var oldTopics storage.RepoAITopics
	db := q.DB.WithContext(ctx)
	if err := db.Find(&old, "repo_id=?", repo.RepoID).Error; err != nil {
		return err
	}
	if err := db.Find(&check, "repo_id=?", repo.RepoID).Error; err != nil {
		return err
	}
	missingTopics := len(repo.GitHubTopics()) == 0 && (p.Manual || q.AI.Summary.AutoDailyCalls > 0)
	if missingTopics {
		if err := db.Find(&oldTopics, "repo_id=?", repo.RepoID).Error; err != nil {
			return err
		}
	}
	eligible, err := q.eligible(ctx, repo, time.Now())
	if err != nil {
		return err
	}
	wantsSummary := func(language string) bool { return p.Manual || (eligible && language != "zh") }
	cachedSummary := old.SummaryStatus == "ready" && q.matches(old, check.SourceFingerprint)
	topicModelMatches := q.matchesTopicModel(oldTopics)
	cachedTopics := topicModelMatches &&
		strings.HasPrefix(oldTopics.SourceFingerprint, check.SourceFingerprint+"|")
	etag := ""
	if !p.Force && check.ReadmeETag != nil && (!wantsSummary(check.SourceLanguage) || cachedSummary) &&
		(!missingTopics || cachedTopics) {
		etag = *check.ReadmeETag
	}
	policy := gh.Policy{RepositoryID: repo.RepoID, Grade: repo.Grade, Priority: p.Manual, ReadmeBudget: q.AI.Summary.GitHubBudget}
	input, unchanged, err := e.readSource(ctx, repo.RepoID, etag, policy)
	// README 返回 304 时仍需检查简介是否变化，变化后重新读取正文。
	if err == nil && unchanged && missingTopics && !input.hasGitHubTopics &&
		oldTopics.SourceFingerprint != topicsFingerprint(check.SourceFingerprint, input.description) {
		input, unchanged, err = e.readSource(ctx, repo.RepoID, "", policy)
	}
	if err != nil {
		return e.sourceFailed(ctx, job, err)
	}
	now := time.Now()
	next := q.nextCheck(repo, now)
	if unchanged {
		check.LastCheckedAt, check.NextCheckAt, check.LastErrorCode = now.UnixMilli(), next, nil
		return q.settle(ctx, job, outcome{status: "succeeded", attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			return saveCheck(tx, check)
		}})
	}
	check = sourceCheck(repo.RepoID, input, now, next)
	summaryMatches := q.matches(old, input.fingerprint)
	makeSummary := wantsSummary(input.language) && (p.Force || !summaryMatches)
	fingerprint := topicsFingerprint(input.fingerprint, input.description)
	makeTopics := missingTopics && !input.hasGitHubTopics && (p.Force || !topicModelMatches || oldTopics.SourceFingerprint != fingerprint)
	if (!makeSummary && !makeTopics) || (input.body == "" && input.description == "") {
		return q.settle(ctx, job, outcome{status: "succeeded", attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			if summaryMatches && old.SummaryStatus != "ready" {
				if err := tx.Model(&old).Update("summary_status", "ready").Error; err != nil {
					return err
				}
			}
			return saveObserved(tx, old, check, fingerprint)
		}})
	}
	// 正文取得后再次确认公开性和 canonical 地址，名称被复用的内容不得发送。
	confirmed, err := e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return e.sourceFailed(ctx, job, err)
	}
	if !strings.EqualFold(confirmed.GetFullName(), input.fullName) {
		return e.fail(ctx, job, "summary_repo_address_changed", true, 0, false)
	}
	makeTopics = makeTopics && len(confirmed.GetTopics()) == 0
	if !makeSummary && !makeTopics {
		return q.settle(ctx, job, outcome{status: "succeeded", attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check, fingerprint)
		}})
	}
	prompt, sent := input.prompt(q.AI.Summary.InputTokens, makeSummary, makeTopics)
	if sent.body == "" && sent.description == "" {
		return e.fail(ctx, job, "summary_input_budget_too_small", false, 0, false)
	}
	reservedAt := time.Now()
	call, err := q.reserve(ctx, job, p.Manual, reservedAt)
	if errors.Is(err, errBudget) {
		nextDay := reservedAt.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli()
		return q.settle(ctx, job, outcome{status: "failed", code: "ai_budget_exhausted", next: nextDay, attempts: job.Attempts - 1, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check, fingerprint)
		}})
	}
	if err != nil {
		return err
	}
	if err := q.sending(ctx, job, call); err != nil {
		return err
	}
	start := time.Now()
	result := e.Provider.Generate(ctx, prompt)
	duration := time.Since(start)
	completed := time.Now()
	var record storage.AISummary
	var topicsJSON string
	valid := true
	if makeSummary {
		record, valid = resultSummary(result.Content, sent, repo.RepoID, call.Provider, q.AI.Model, q.AI.Summary.PromptVersion, completed.UnixMilli())
	}
	if makeTopics {
		var topicsValid bool
		topicsJSON, topicsValid = resultTopics(result.Content)
		// 同一请求里的有效摘要可以独立保存，缺失主题词留给下次检查。
		valid = valid && (makeSummary || topicsValid)
	}
	if result.Code == "" && !valid {
		result.Code, result.Retry = "invalid_output", true
	}
	if result.Code != "" {
		return e.finishFailure(ctx, job, call, result, duration, old, check, fingerprint)
	}

	// 供应商返回后重新确认真实来源，防止分析期间 README 被替换。
	latest, same, verifyErr := e.readSource(ctx, repo.RepoID, input.etag, policy)
	changed := !same && verifyErr == nil && latest.fingerprint != input.fingerprint
	observedFingerprint := fingerprint
	if verifyErr == nil {
		if same {
			latest.fingerprint = input.fingerprint
		}
		observedFingerprint = topicsFingerprint(latest.fingerprint, latest.description)
	}
	topicsChanged := observedFingerprint != fingerprint
	if changed {
		check = sourceCheck(repo.RepoID, latest, time.Now(), time.Now().UnixMilli())
		record.SummaryStatus = "stale"
	}
	if makeTopics && topicsChanged {
		check.NextCheckAt = completed.UnixMilli()
	}
	if verifyErr != nil {
		check.NextCheckAt = completed.Add(time.Minute).UnixMilli()
		record.SummaryStatus = "stale"
	}
	if errors.Is(verifyErr, ErrRepoUnavailable) {
		// 已不可见的仓库不保存本轮新摘要，调用账目仍按实际结果提交。
		return e.settleAfterCall(ctx, job, outcome{status: "dead", code: "summary_repo_unavailable",
			attempts: job.Attempts, call: &call, model: result, duration: duration,
			apply: withdrawRepository(verifyErr, repo.RepoID)})
	}
	if makeSummary && record.SummaryStatus == "stale" && old.RepoID != 0 && verifyErr == nil {
		record = old
		record.SummaryStatus = "stale"
	}
	status, code := "succeeded", ""
	if (changed || (!makeSummary && topicsChanged)) && p.Manual {
		status, code = "failed", "summary_source_changed"
		if job.Attempts >= job.MaxAttempts {
			status = "dead"
		}
	}
	return e.settleAfterCall(ctx, job, outcome{status: status, code: code, attempts: job.Attempts, call: &call, model: result, duration: duration,
		apply: func(tx *gorm.DB) error {
			if err := saveObserved(tx, old, check, observedFingerprint); err != nil {
				return err
			}
			if makeSummary {
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&record).Error; err != nil {
					return err
				}
			} else if summaryMatches && !changed && verifyErr == nil && old.SummaryStatus != "ready" {
				if err := tx.Model(&old).Update("summary_status", "ready").Error; err != nil {
					return err
				}
			}
			if topicsJSON != "" && verifyErr == nil && !topicsChanged && !latest.hasGitHubTopics {
				topics := storage.RepoAITopics{RepoID: repo.RepoID, TopicsJSON: topicsJSON, SourceFingerprint: fingerprint,
					ModelProvider: call.Provider, ModelName: q.AI.Model, PromptVersion: q.AI.Summary.PromptVersion, GeneratedAt: completed.UnixMilli()}
				return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&topics).Error
			}
			return nil
		}})
}

func sourceCheck(id int64, s source, now time.Time, next int64) storage.SummaryCheck {
	return storage.SummaryCheck{RepoID: id, ReadmeETag: optional(s.etag), LastSeenReadmeSHA: optional(s.sha),
		SourceFingerprint: s.fingerprint, SourceLanguage: s.language, LastCheckedAt: now.UnixMilli(), NextCheckAt: next}
}

func saveCheck(tx *gorm.DB, c storage.SummaryCheck) error {
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&c).Error
}

func saveObserved(tx *gorm.DB, old storage.AISummary, check storage.SummaryCheck, topicsFingerprint string) error {
	if err := saveCheck(tx, check); err != nil {
		return err
	}
	// 已确认来源变化时停止使用旧 AI Topics，失败或额度用尽时也生效。
	if err := tx.Where("repo_id=? AND source_fingerprint<>?", check.RepoID, topicsFingerprint).
		Delete(&storage.RepoAITopics{}).Error; err != nil {
		return err
	}
	if old.RepoID != 0 && old.SourceFingerprint != check.SourceFingerprint {
		return tx.Model(&old).Update("summary_status", "stale").Error
	}
	return nil
}

// publicRepo 用数字 ID 确认可见性、仓库身份及当前地址。
func (e *Engine) publicRepo(ctx context.Context, id int64, p gh.Policy) (*sdk.Repository, error) {
	if err := e.checkRepoUnblocked(ctx, id); err != nil {
		return nil, err
	}
	remote, err := e.GitHub.RepositoryByID(ctx, id, p)
	// 身份请求等待期间可能发生屏蔽，继续读取正文或发布前重新确认。
	if blocked := e.checkRepoUnblocked(ctx, id); blocked != nil {
		return nil, blocked
	}
	observed := time.Now().UnixMilli()
	if err != nil {
		var upstream *gh.Failure
		if errors.As(err, &upstream) && (upstream.Status == 404 || upstream.Status == 410) {
			return nil, &visibilityFailure{observed: observed, gone: upstream.Status == 410}
		}
		return nil, err
	}
	r := remote.Repository
	if r == nil || r.GetID() != id {
		return nil, ErrRepoUnavailable
	}
	if r.GetPrivate() || r.GetDisabled() || (r.GetVisibility() != "" && r.GetVisibility() != "public") {
		return nil, &visibilityFailure{observed: observed, gone: true}
	}
	return r, nil
}

func (e *Engine) checkRepoUnblocked(ctx context.Context, id int64) error {
	var count int64
	if err := e.Queue.DB.WithContext(ctx).Model(&storage.Repository{}).Where("repo_id=? AND blocked=1", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return storage.ErrRepositoryBlocked
	}
	return nil
}

func (e *Engine) readSource(ctx context.Context, id int64, etag string, p gh.Policy) (source, bool, error) {
	r, err := e.publicRepo(ctx, id, p)
	if err != nil {
		return source{}, false, err
	}
	owner, name, ok := strings.Cut(r.GetFullName(), "/")
	if !ok {
		return source{}, false, ErrRepoUnavailable
	}
	readme, err := e.GitHub.Readme(ctx, owner, name, etag, e.Queue.AI.Summary.ReadmeMaxBytes, p)
	if err != nil {
		return source{}, false, err
	}
	s := source{fullName: r.GetFullName(), description: strings.Join(strings.Fields(r.GetDescription()), " "),
		hasGitHubTopics: len(r.GetTopics()) > 0, quality: sufficient}
	if readme.NotModified && etag != "" {
		return s, true, nil
	}
	if readme.NotModified {
		if err := e.checkRepoUnblocked(ctx, id); err != nil {
			return source{}, false, err
		}
		readme, err = e.GitHub.Readme(ctx, owner, name, "", e.Queue.AI.Summary.ReadmeMaxBytes, p)
		if err != nil {
			return source{}, false, err
		}
		if readme.NotModified {
			return source{}, false, &gh.Failure{Code: "github_unexpected_readme_304", Status: 304}
		}
	}
	if readme.Missing {
		s.fingerprint, s.quality = descriptionKey(s.description), insufficient
	} else {
		s.sha, s.etag, s.url = readme.SHA, readme.ETag, readme.URL
		s.fingerprint = "readme:" + s.sha
		s.body = prose(readme.Body)
		if readme.Truncated {
			s.quality = incomplete
		} else if utf8.RuneCountInString(s.body) < 200 {
			s.quality = insufficient
		}
	}
	natural := s.body
	if natural == "" {
		natural = s.description
	}
	s.language = language(natural)
	return s, false, nil
}
