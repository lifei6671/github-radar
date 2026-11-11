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
	Queue    Queue
	GitHub   GitHubSource
	Provider Provider
	Logger   *slog.Logger
	Tick     time.Duration
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
	if repo.Disabled || !repo.NameIsCurrent || repo.State == "unavailable" {
		return e.fail(ctx, job, "summary_repo_unavailable", false, 0, false)
	}
	var old storage.AISummary
	var check storage.SummaryCheck
	db := q.DB.WithContext(ctx)
	if err := db.Find(&old, "repo_id=?", repo.RepoID).Error; err != nil {
		return err
	}
	if err := db.Find(&check, "repo_id=?", repo.RepoID).Error; err != nil {
		return err
	}
	etag := ""
	if !p.Force && old.SummaryStatus == "ready" && q.matches(old, check.SourceFingerprint) && check.ReadmeETag != nil {
		etag = *check.ReadmeETag
	}
	policy := gh.Policy{Grade: repo.Grade, Priority: p.Manual, ReadmeBudget: q.AI.Summary.GitHubBudget}
	input, unchanged, err := e.readSource(ctx, repo.RepoID, etag, policy)
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
	if !p.Force && q.matches(old, input.fingerprint) {
		return q.settle(ctx, job, outcome{status: "succeeded", attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			if err := saveCheck(tx, check); err != nil {
				return err
			}
			return tx.Model(&old).Update("summary_status", "ready").Error
		}})
	}
	eligible, err := q.eligible(ctx, repo, now)
	if err != nil {
		return err
	}
	if (!p.Manual && (!eligible || input.language == "zh")) || (input.body == "" && input.description == "") {
		return q.settle(ctx, job, outcome{status: "succeeded", attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check)
		}})
	}
	prompt, sent := input.prompt(q.AI.Summary.InputTokens)
	if sent.body == "" && sent.description == "" {
		return e.fail(ctx, job, "summary_input_budget_too_small", false, 0, false)
	}
	// 正文取得后再次确认公开性和 canonical 地址，名称被复用的内容不得发送。
	confirmed, err := e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return e.sourceFailed(ctx, job, err)
	}
	if !strings.EqualFold(confirmed.GetFullName(), input.fullName) {
		return e.fail(ctx, job, "summary_repo_address_changed", true, 0, false)
	}
	reservedAt := time.Now()
	call, err := q.reserve(ctx, job, p.Manual, reservedAt)
	if errors.Is(err, errBudget) {
		nextDay := reservedAt.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli()
		return q.settle(ctx, job, outcome{status: "failed", code: "ai_budget_exhausted", next: nextDay, attempts: job.Attempts - 1, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check)
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
	record, valid := resultSummary(result.Content, sent, repo.RepoID, call.Provider, q.AI.Model, q.AI.Summary.PromptVersion, completed.UnixMilli())
	if result.Code == "" && !valid {
		result.Code, result.Retry = "invalid_output", true
	}
	if result.Code != "" {
		return e.finishFailure(ctx, job, call, result, duration, old, check)
	}

	// 供应商返回后重新确认真实来源，防止分析期间 README 被替换。
	latest, same, verifyErr := e.readSource(ctx, repo.RepoID, input.etag, policy)
	changed := !same && verifyErr == nil && latest.fingerprint != input.fingerprint
	if changed {
		check = sourceCheck(repo.RepoID, latest, time.Now(), time.Now().UnixMilli())
		record.SummaryStatus = "stale"
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
	if record.SummaryStatus == "stale" && old.RepoID != 0 && verifyErr == nil {
		record = old
		record.SummaryStatus = "stale"
	}
	status, code := "succeeded", ""
	if changed && p.Manual {
		status, code = "failed", "summary_source_changed"
		if job.Attempts >= job.MaxAttempts {
			status = "dead"
		}
	}
	return e.settleAfterCall(ctx, job, outcome{status: status, code: code, attempts: job.Attempts, call: &call, model: result, duration: duration,
		apply: func(tx *gorm.DB) error {
			if err := saveCheck(tx, check); err != nil {
				return err
			}
			return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&record).Error
		}})
}

func sourceCheck(id int64, s source, now time.Time, next int64) storage.SummaryCheck {
	return storage.SummaryCheck{RepoID: id, ReadmeETag: optional(s.etag), LastSeenReadmeSHA: optional(s.sha),
		SourceFingerprint: s.fingerprint, SourceLanguage: s.language, LastCheckedAt: now.UnixMilli(), NextCheckAt: next}
}

func saveCheck(tx *gorm.DB, c storage.SummaryCheck) error {
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&c).Error
}

func saveObserved(tx *gorm.DB, old storage.AISummary, check storage.SummaryCheck) error {
	if err := saveCheck(tx, check); err != nil {
		return err
	}
	if old.RepoID != 0 && old.SourceFingerprint != check.SourceFingerprint {
		return tx.Model(&old).Update("summary_status", "stale").Error
	}
	return nil
}

// publicRepo 用数字 ID 确认可见性、仓库身份及当前地址。
func (e *Engine) publicRepo(ctx context.Context, id int64, p gh.Policy) (*sdk.Repository, error) {
	remote, err := e.GitHub.RepositoryByID(ctx, id, p)
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
	if readme.NotModified && etag != "" {
		return source{}, true, nil
	}
	if readme.NotModified {
		readme, err = e.GitHub.Readme(ctx, owner, name, "", e.Queue.AI.Summary.ReadmeMaxBytes, p)
		if err != nil {
			return source{}, false, err
		}
		if readme.NotModified {
			return source{}, false, &gh.Failure{Code: "github_unexpected_readme_304", Status: 304}
		}
	}
	s := source{fullName: r.GetFullName(), description: strings.Join(strings.Fields(r.GetDescription()), " "), quality: sufficient}
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
