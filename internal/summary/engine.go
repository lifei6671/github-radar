package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/google/go-github/v88/github"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
)

type GitHubSource interface {
	RepositoryByID(context.Context, int64, gh.Policy) (gh.RepositoryResult, error)
	Readme(context.Context, string, string, string, int, gh.Policy) (gh.ReadmeResult, error)
	ReadmeSHA(context.Context, string, string, string, gh.Policy) (string, error)
}

type Engine struct {
	Queue       Queue
	GitHub      GitHubSource
	Provider    Provider
	Translate   func(context.Context, string) ModelResult
	CleanReadme func(context.Context, string) ModelResult
	WriteReport func(context.Context, string) ModelResult
	Logger      *slog.Logger
	Tick        time.Duration
}

func (e *Engine) Run(ctx context.Context) error {
	for !lifecycle.Stopping(ctx) {
		ready, err := storage.Initialized(ctx, e.Queue.DB)
		if ready && err == nil {
			break
		}
		if err != nil && !storage.IsBusy(err) {
			return err
		}
		if !lifecycle.Wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
	if lifecycle.Stopping(ctx) {
		return ctx.Err()
	}
	group, workerCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		for !lifecycle.Stopping(workerCtx) {
			now := time.Now()
			err := e.Queue.Recover(workerCtx, now)
			if err == nil && !lifecycle.Stopping(workerCtx) && e.GitHub != nil {
				queue := e.Queue
				if e.Provider == nil {
					queue.AI.Summary.AutoDailyCalls = 0
				}
				err = queue.Schedule(workerCtx, now)
				if err == nil && e.CleanReadme != nil {
					err = queue.scheduleReadmeCleanup(workerCtx, now)
				}
			}
			if err != nil && workerCtx.Err() == nil {
				e.Logger.Warn("摘要调度失败", "error", err)
			}
			if !lifecycle.Wait(workerCtx, e.Tick) {
				return workerCtx.Err()
			}
		}
		return workerCtx.Err()
	})
	if e.GitHub != nil || e.WriteReport != nil {
		for range e.Queue.AI.Summary.Workers {
			group.Go(func() error {
				for !lifecycle.Stopping(workerCtx) {
					done, err := e.Step(workerCtx)
					if err != nil && !errors.Is(err, errLease) && workerCtx.Err() == nil {
						// 模型及 GitHub 错误已转为固定码，日志只接收数据库等本地错误。
						e.Logger.Warn("摘要 Worker 未完成", "error", err)
					}
					if (!done || err != nil) && !lifecycle.Wait(workerCtx, time.Second) {
						return workerCtx.Err()
					}
				}
				return workerCtx.Err()
			})
		}
	}
	return group.Wait()
}

func (e *Engine) Step(ctx context.Context) (bool, error) {
	job, localOnly, err := e.Queue.claimWork(ctx, time.Now(), e.Provider != nil || e.Translate != nil || e.CleanReadme != nil || e.WriteReport != nil, e.GitHub != nil, e.Provider != nil, e.Translate != nil)
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
					Update(storage.JobColumns.LeaseUntil, now.Add(time.Duration(e.Queue.LeaseSeconds)*time.Second).UnixMilli())); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = e.runJob(taskCtx, *job, localOnly)
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

func (e *Engine) runJob(ctx context.Context, job storage.Job, localOnly bool) error {
	if job.Type == ReportJob {
		return e.runReport(ctx, job)
	}
	q := e.Queue
	var p payload
	if json.Unmarshal([]byte(job.PayloadJSON), &p) != nil {
		return e.fail(ctx, job, consts.CodeSummaryInvalidJob, false, 0, false)
	}
	var repo storage.Repository
	if err := q.DB.WithContext(ctx).Take(&repo, storage.RepositoryColumns.RepoID+"=?", *job.RepoID).Error; err != nil {
		return err
	}
	if !repo.IsPublic() {
		return e.fail(ctx, job, consts.CodeSummaryRepoUnavailable, false, 0, false)
	}
	if !p.Manual && !q.AI.Summary.LegacyAdmission && jobOperation(job) != "verify" {
		d, _, err := storage.AIDecision(q.DB.WithContext(ctx), repo, jobOperation(job), false, time.Now())
		if err != nil {
			return err
		}
		if d.State != admission.Pass {
			return q.stopAdmission(ctx, job, d)
		}
	}
	if job.Type == TranslationJob {
		return e.runTranslation(ctx, job, repo, p, localOnly)
	}
	var old storage.AISummary
	var check storage.SummaryCheck
	var oldTopics storage.RepoAITopics
	db := q.DB.WithContext(ctx)
	if err := db.Find(&old, storage.AISummaryColumns.RepoID+"=?", repo.RepoID).Error; err != nil {
		return err
	}
	if err := db.Find(&check, storage.SummaryCheckColumns.RepoID+"=?", repo.RepoID).Error; err != nil {
		return err
	}
	withTopics := e.Provider != nil && (p.Manual || q.AI.Summary.AutoDailyCalls > 0)
	var catalog storage.ThemeCatalog
	var vocabulary storage.TechnicalTags
	if withTopics {
		var err error
		catalog, err = storage.ReadThemeCatalog(db)
		if err != nil {
			return err
		}
		vocabulary, err = storage.ReadTechnicalTags(db)
		if err != nil {
			return err
		}
		if err := db.Find(&oldTopics, storage.RepoAITopicsColumns.RepoID+"=?", repo.RepoID).Error; err != nil {
			return err
		}
	}
	eligible, err := q.eligible(ctx, repo, time.Now())
	if err != nil {
		return err
	}
	wantsSummary := e.Provider != nil && (p.Manual || eligible || (q.AI.Summary.LegacyAdmission && old.RepoID != 0 && q.AI.Summary.AutoDailyCalls > 0))
	cachedSummary := old.SummaryStatus == consts.StatusReady && hasStructuredSummary(old) && q.matches(old, check.SourceFingerprint)
	topicModelMatches := q.matchesTopicModel(oldTopics) && oldTopics.ThemeCatalogKey == catalog.Key
	cachedTopics := topicModelMatches &&
		strings.HasPrefix(oldTopics.SourceFingerprint, check.SourceFingerprint+"|")
	if !p.Manual && withTopics && !topicModelMatches && old.SummaryStatus == consts.StatusReady && (q.AI.Summary.LegacyAdmission || p.TopicsOnly) {
		if err := q.setTopicScope(ctx, &job, true); err != nil {
			return err
		}
	}
	etag := ""
	if !p.Force && check.ReadmeETag != nil && (!wantsSummary || cachedSummary) &&
		(!withTopics || cachedTopics) {
		var cached int64
		limit := q.AI.Summary.ReadmeMaxBytes
		if err := db.Model(&storage.RepoReadmeDocument{}).Where("repo_id=? AND readme_sha=?", repo.RepoID, check.LastSeenReadmeSHA).
			Where("readme_bytes<=? AND (truncated=0 OR readme_bytes=?)", limit, limit).
			Count(&cached).Error; err != nil {
			return err
		}
		if cached != 0 {
			etag = *check.ReadmeETag
		}
	}
	policy := gh.Policy{RepositoryID: repo.RepoID, Grade: repo.Grade, Priority: p.Manual, ReadmeBudget: q.AI.Summary.GitHubBudget}
	var input source
	var usingCached, unchanged bool
	if withTopics && !p.Force && !topicModelMatches && old.SummaryStatus == consts.StatusReady &&
		((!p.Manual && (q.AI.Summary.LegacyAdmission || p.TopicsOnly)) || (p.Manual && cachedSummary && oldTopics.TagVocabVersion != storage.TagVocabularyVersion)) {
		input, usingCached, err = cachedTopicSource(db, repo, old, check)
		if err != nil {
			return err
		}
	}
	if !usingCached {
		if localOnly {
			// 正文或主题词表在领取后变化时，交回常规队列重新验证来源。
			now := time.Now()
			return q.settle(ctx, job, outcome{status: consts.StatusPending, attempts: job.Attempts - 1, next: retryAt(job, now)})
		}
		input, unchanged, err = e.readSummarySource(ctx, job, etag, policy)
	}
	// README 返回 304 时仍需检查简介是否变化，变化后重新读取正文。
	if err == nil && unchanged && withTopics &&
		(!topicModelMatches || oldTopics.SourceFingerprint != topicsFingerprint(check.SourceFingerprint, input.description)) {
		input, unchanged, err = e.readSource(ctx, repo.RepoID, "", policy)
	}
	if err != nil {
		return e.sourceFailed(ctx, job, err)
	}
	now := time.Now()
	next := q.nextCheck(repo, now)
	if unchanged {
		check.LastCheckedAt, check.NextCheckAt, check.LastErrorCode = now.UnixMilli(), next, nil
		return q.settle(ctx, job, outcome{status: consts.StatusSucceeded, attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			return saveCheck(tx, check)
		}})
	}
	if !usingCached {
		check = sourceCheck(repo.RepoID, input, now, next)
		if err := e.rememberReadme(ctx, job, input.readme, input.fullName); err != nil {
			return err
		}
	}
	summaryMatches := q.matches(old, input.fingerprint)
	if summaryMatches && old.SummaryStatus == consts.StatusStale && old.GeneratedAt >= job.CreatedAt && input.sha != "" {
		// 上一轮已收到有效导读，只有发布复核未完成。继续验证同版材料，无需重调模型。
		if _, err := e.verifyReadmeVersion(ctx, repo.RepoID, input.fullName, input.readme, policy); err != nil {
			if !readmeAddressChanged(err) {
				return e.sourceFailed(ctx, job, err)
			}
			return q.settle(ctx, job, staleSummarySource(job, input.sha, outcome{attempts: job.Attempts}))
		}
		return q.settle(ctx, job, outcome{status: consts.StatusSucceeded, attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			if err := saveObserved(tx, old, check, topicsFingerprint(input.fingerprint, input.description)); err != nil {
				return err
			}
			return publishGuideStatus(tx, old, now.UnixMilli())
		}})
	}
	unchangedGuide := jobTopicsOnly(job) && old.SummaryStatus == consts.StatusReady && old.SourceFingerprint == input.fingerprint
	makeSummary := !usingCached && !unchangedGuide && wantsSummary && (p.Force || !summaryMatches || !hasStructuredSummary(old))
	fingerprint := topicsFingerprint(input.fingerprint, input.description)
	makeTopics := withTopics && (makeSummary || p.Force || !topicModelMatches || oldTopics.SourceFingerprint != fingerprint)
	if (makeSummary || makeTopics || (!cachedSummary && wantsSummary)) &&
		!q.AI.Summary.LegacyAdmission && !p.Manual && !repo.StarredByUser &&
		!admission.Readable(input.body, repo.Name) && !admission.Readable(input.description, repo.Name) {
		return q.stopAdmission(ctx, job, admission.Decision{State: admission.Defer, Reason: "verified_no_readable_source",
			RetryAt: now.Add(7 * 24 * time.Hour).UnixMilli()})
	}
	if (!makeSummary && !makeTopics) || (input.body == "" && input.description == "") {
		return q.settle(ctx, job, outcome{status: consts.StatusSucceeded, attempts: job.Attempts, apply: func(tx *gorm.DB) error {
			if summaryMatches && old.SummaryStatus != consts.StatusReady {
				if err := publishGuideStatus(tx, old, now.UnixMilli()); err != nil {
					return err
				}
			}
			if !p.Manual && !q.AI.Summary.LegacyAdmission && summaryMatches {
				return errors.Join(saveObserved(tx, old, check, fingerprint),
					storage.SaveAdmission(tx, repo.RepoID, "cache", admission.Decision{State: admission.Pass, Reason: "fingerprint_cached"}, admission.Evidence{}, now))
			}
			return saveObserved(tx, old, check, fingerprint)
		}})
	}
	if err := q.setTopicScope(ctx, &job, makeTopics && !makeSummary); err != nil {
		return err
	}
	input.themePrompt = themePrompt(catalog, q.AI.Summary.InputTokens/4)
	input.technicalTags = vocabulary
	// 选题材料为可选输出，给低输出预算保留导读及译文所需空间。
	editorial := q.AI.Summary.OutputTokens >= 1536
	prompt, sent := input.prompt(q.AI.Summary.InputTokens, makeSummary, makeTopics, editorial)
	if sent.body == "" && sent.description == "" {
		return e.fail(ctx, job, consts.CodeSummaryInputBudgetTooSmall, false, 0, false)
	}
	var segments []string
	var translation storage.RepoReadmeTranslation
	if makeSummary {
		segments, translation, err = q.firstReadmeBatch(db, repo.RepoID, input)
		if err != nil {
			return err
		}
		if len(segments) > 0 {
			encoded, _ := json.Marshal(segments)
			basePrompt, baseSource := input.prompt(q.AI.Summary.InputTokens-len(combinedTranslationInstruction)-len(encoded)-1,
				makeSummary, makeTopics, editorial && q.AI.Summary.OutputTokens >= 3072)
			if len(basePrompt)+len(systemPrompt)+len(combinedTranslationInstruction)+len(encoded)+1 > q.AI.Summary.InputTokens || (baseSource.body == "" && baseSource.description == "") {
				segments = nil
			} else {
				task, evidence, _ := strings.Cut(basePrompt, "\n")
				prompt, sent = task+combinedTranslationInstruction+string(encoded)+"\n"+evidence, baseSource
			}
		}
	}
	reservedAt := time.Now()
	call, err := q.reserve(ctx, job, p.Manual, reservedAt)
	if errors.Is(err, errBudget) {
		return q.settle(ctx, job, outcome{status: consts.StatusFailed, code: consts.CodeAIBudgetExhausted, next: budgetRetryAt(job, reservedAt), attempts: job.Attempts - 1, apply: func(tx *gorm.DB) error {
			if usingCached {
				return nil
			}
			return saveObserved(tx, old, check, fingerprint)
		}})
	}
	if err != nil {
		return err
	}
	operation := jobOperation(job)
	if makeSummary && old.RepoID == 0 {
		operation = "summary_first"
	}
	sendCtx, err := q.sending(ctx, job, call, fmt.Sprintf("%d:%s", q.AI.Summary.PromptVersion, fingerprint), operation)
	if err != nil {
		return q.sendingFailure(ctx, job, call, err)
	}
	start := time.Now()
	result := e.Provider.Generate(sendCtx, prompt)
	duration := time.Since(start)
	completed := time.Now()
	var record storage.AISummary
	var topicsJSON, themeSlugs, themeKey, tagSlugs, taxonomyJSON string
	keepPreviousClassification := false
	valid := true
	if makeSummary {
		record, valid = resultSummary(result.Content, sent, repo.RepoID, call.Provider, q.AI.Model, q.AI.Summary.PromptVersion, completed.UnixMilli())
	}
	if makeSummary && old.RepoID != 0 {
		// 旧版自由标签只作为历史数据保留，不再接受模型写入。
		record.ValueTagsJSON = old.ValueTagsJSON
		if old.SourceFingerprint == input.fingerprint {
			record.ValueTagSlugsJSON = old.ValueTagSlugsJSON
		}
	}
	if makeTopics {
		classification, issue := readTaxonomyResult(result.Content, sent)
		topicsJSON = classification.topics()
		var themesValid bool
		themeSlugs, themesValid = resultThemeSlugs(result.Content, topicsJSON, catalog)
		// 导读与分类分开验收；没有原文证据的主能力不会覆盖旧分类。
		if issue == "" && themesValid {
			// 合法留空是一次已完成的判断。记录待复核，同时保留历史分类的审核状态。
			keepPreviousClassification = classification.Primary == "" && oldTopics.RepoID != 0 && oldTopics.TopicsJSON != "[]" && oldTopics.TopicsJSON != ""
			themeKey = catalog.Key
			tagSlugs = classification.cardTags()
			encoded, _ := json.Marshal(classification)
			taxonomyJSON = string(encoded)
		}
		if issue == "" && !themesValid {
			issue = "taxonomy_invalid_themes"
		}
		if issue != "" && result.Code == "" {
			// 只记录固定错误类别，避免把未审核的模型响应写入日志。
			e.Logger.Info("技术用途未通过校验", "job_id", job.ID, "issue", issue)
			if result.OutputIssue == "" {
				result.OutputIssue = issue
			}
		}
		valid = valid && (makeSummary || issue == "")
	}
	if result.Code == "" && !valid {
		result.Code, result.Retry = consts.CodeInvalidOutput, true
		if result.OutputIssue == "" {
			result.OutputIssue = "summary_validation_failed"
		}
	}
	if result.Code != "" {
		if usingCached {
			check = storage.SummaryCheck{}
		}
		return e.finishFailure(ctx, job, call, result, duration, old, check, fingerprint)
	}

	// 发布前通过文件元数据复核版本，并检查仓库身份和简介。
	latest, same, verifyErr := input, true, error(nil)
	if makeSummary && input.sha != "" {
		var remote *sdk.Repository
		remote, verifyErr = e.verifyReadmeVersion(ctx, repo.RepoID, input.fullName, input.readme, policy)
		if verifyErr == nil {
			latest.description = strings.Join(strings.Fields(remote.GetDescription()), " ")
		}
	} else if !usingCached {
		latest, same, verifyErr = e.readSource(ctx, repo.RepoID, input.etag, policy)
	}
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
		record.SummaryStatus = consts.StatusStale
	}
	if makeTopics && topicsChanged {
		check.NextCheckAt = completed.UnixMilli()
	}
	if verifyErr != nil {
		check.NextCheckAt = completed.Add(time.Minute).UnixMilli()
		record.SummaryStatus = consts.StatusStale
	}
	if errors.Is(verifyErr, ErrRepoUnavailable) {
		// 已不可见的仓库不保存本轮新摘要，调用账目仍按实际结果提交。
		return e.settleAfterCall(ctx, job, outcome{status: consts.StatusDead, code: consts.CodeSummaryRepoUnavailable,
			attempts: job.Attempts, call: &call, model: result, duration: duration,
			apply: withdrawRepository(verifyErr, repo.RepoID)})
	}
	if readmeAddressChanged(verifyErr) && makeSummary && input.sha != "" {
		return e.settleAfterCall(ctx, job, staleSummarySource(job, input.sha, outcome{
			attempts: job.Attempts, call: &call, model: result, duration: duration}))
	}
	if makeSummary && record.SummaryStatus == consts.StatusStale && old.RepoID != 0 && verifyErr == nil {
		record = old
		record.SummaryStatus = consts.StatusStale
	}
	status, code := consts.StatusSucceeded, ""
	var retryNext int64
	attempts := job.Attempts
	if (makeSummary && input.sha != "" && verifyErr != nil) || ((changed || (!makeSummary && topicsChanged)) && p.Manual) {
		status, code = consts.StatusFailed, consts.CodeSummarySourceChanged
		retryNext = retryAt(job, completed)
		var upstream *gh.Failure
		if errors.As(verifyErr, &upstream) && upstream.Until > 0 {
			retryNext = max(retryNext, upstream.Until)
			if upstream.Status == 0 {
				attempts--
			}
		}
		if attempts >= job.MaxAttempts {
			status = consts.StatusDead
		}
	}
	return e.settleAfterCall(ctx, job, outcome{status: status, code: code, next: retryNext, attempts: attempts, call: &call, model: result, duration: duration,
		apply: func(tx *gorm.DB) error {
			if usingCached {
				current, err := cachedTopicSourceCurrent(tx, repo.RepoID, input)
				if err != nil || !current {
					return err
				}
			} else {
				if err := saveObserved(tx, old, check, observedFingerprint); err != nil {
					return err
				}
			}
			if !same && verifyErr == nil {
				if err := saveReadme(tx, repo.RepoID, latest.readme, latest.fullName, job.ID); err != nil {
					return err
				}
			}
			if makeSummary {
				if keepPreviousClassification {
					record.ValueTagSlugsJSON = old.ValueTagSlugsJSON
				}
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&record).Error; err != nil {
					return err
				}
				if record.SummaryStatus == consts.StatusReady {
					if err := storage.RecordFirstGuide(tx, repo.RepoID, time.Now().UnixMilli()); err != nil {
						return err
					}
				}
			} else if summaryMatches && !changed && verifyErr == nil && old.SummaryStatus != consts.StatusReady {
				if err := publishGuideStatus(tx, old, time.Now().UnixMilli()); err != nil {
					return err
				}
			}
			if len(segments) > 0 {
				// 复核暂时失败时保存有效片段，待仓库重新确认后再发布正文。
				if err := saveCombinedTranslation(tx, translation, result.Content, segments, verifyErr == nil); err != nil {
					return err
				}
				if translation.Complete && verifyErr == nil {
					if err := saveReadme(tx, repo.RepoID, input.readme, input.fullName, job.ID); err != nil {
						return err
					}
				}
			}
			if topicsJSON != "" && themeKey != "" && verifyErr == nil && !topicsChanged {
				if usingCached && check.LastErrorCode != nil {
					// 缓存回填成功后清除上次检查的分类错误，不改来源检查时间。
					if err := tx.Model(&check).Update(storage.SummaryCheckColumns.LastErrorCode, nil).Error; err != nil {
						return err
					}
				}
				// 与分类同事务发布标签；独立回填沿用已发布的导读正文和时间。
				topics := storage.RepoAITopics{RepoID: repo.RepoID, TopicsJSON: topicsJSON, SourceFingerprint: fingerprint,
					ThemeSlugsJSON: themeSlugs, ThemeCatalogKey: themeKey, PurposeVersion: storage.TechnicalPurposeVersion, TagVocabVersion: storage.TagVocabularyVersion, TaxonomyJSON: taxonomyJSON,
					ModelProvider: call.Provider, ModelName: q.AI.Model, PromptVersion: q.AI.Summary.PromptVersion, GeneratedAt: completed.UnixMilli()}
				if keepPreviousClassification {
					topics.TopicsJSON, topics.PurposeVersion = oldTopics.TopicsJSON, oldTopics.PurposeVersion
					if oldTopics.ThemeCatalogKey == catalog.Key {
						topics.ThemeSlugsJSON = oldTopics.ThemeSlugsJSON
					}
				} else if err := tx.Model(&storage.AISummary{}).Where("repo_id = ? AND summary_status = ? AND source_fingerprint = ?", repo.RepoID, consts.StatusReady, input.fingerprint).
					Update(storage.AISummaryColumns.ValueTagSlugsJSON, tagSlugs).Error; err != nil {
					return err
				}
				return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&topics).Error
			}
			return nil
		}})
}

// publishGuideStatus 复核旧输出后第一次可用时才计入首次清洗。
func publishGuideStatus(tx *gorm.DB, row storage.AISummary, at int64) error {
	if err := tx.Model(&row).Update(storage.AISummaryColumns.SummaryStatus, consts.StatusReady).Error; err != nil {
		return err
	}
	return storage.RecordFirstGuide(tx, row.RepoID, at)
}

func staleSummarySource(job storage.Job, sha string, out outcome) outcome {
	now := time.Now()
	out.status, out.code, out.next = consts.StatusFailed, consts.CodeSummarySourceChanged, retryAt(job, now)
	if job.Attempts >= job.MaxAttempts {
		out.status = consts.StatusDead
	}
	out.apply = func(tx *gorm.DB) error { return invalidateReadme(tx, *job.RepoID, sha, now.UnixMilli()) }
	return out
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
	if err := tx.Where(storage.RepoAITopicsColumns.RepoID+"=? AND "+storage.RepoAITopicsColumns.SourceFingerprint+"<>?", check.RepoID, topicsFingerprint).
		Delete(&storage.RepoAITopics{}).Error; err != nil {
		return err
	}
	if old.RepoID != 0 && old.SourceFingerprint != check.SourceFingerprint {
		return tx.Model(&old).Update(storage.AISummaryColumns.SummaryStatus, consts.StatusStale).Error
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
	if err := e.Queue.DB.WithContext(ctx).Model(&storage.Repository{}).Where(storage.RepositoryColumns.RepoID+"=? AND "+storage.RepositoryColumns.Blocked+"=1", id).Count(&count).Error; err != nil {
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
	readme, err := e.preferredReadme(ctx, owner, name, etag, p)
	if err != nil {
		return source{}, false, err
	}
	s := source{fullName: r.GetFullName(), description: strings.Join(strings.Fields(r.GetDescription()), " "),
		githubTopics: r.GetTopics(), quality: sufficient}
	if readme.NotModified && etag != "" {
		return s, true, nil
	}
	if readme.NotModified {
		if err := e.checkRepoUnblocked(ctx, id); err != nil {
			return source{}, false, err
		}
		readme, err = e.preferredReadme(ctx, owner, name, "", p)
		if err != nil {
			return source{}, false, err
		}
		if readme.NotModified {
			return source{}, false, &gh.Failure{Code: consts.CodeGitHubUnexpectedReadme304, Status: 304}
		}
	}
	// 确认读取期间仓库仍公开，且路径没有被其他仓库复用。
	confirmed, err := e.publicRepo(ctx, id, p)
	if err != nil {
		return source{}, false, err
	}
	if !strings.EqualFold(confirmed.GetFullName(), s.fullName) {
		return source{}, false, &gh.Failure{Code: consts.CodeSummaryRepoAddressChanged}
	}
	return describeReadme(confirmed, readme), false, nil
}

// 同一摘要任务重试时继续处理已保存的正文，新任务仍按原有规则检查 GitHub 版本。
func (e *Engine) readSummarySource(ctx context.Context, job storage.Job, etag string, policy gh.Policy) (source, bool, error) {
	var document storage.RepoReadmeDocument
	if err := e.Queue.DB.WithContext(ctx).Find(&document, "repo_id = ? AND source_job_id = ?", *job.RepoID, job.ID).Error; err != nil {
		return source{}, false, err
	}
	if snapshot, ok := savedReadme(document); ok && readmeFits(document, e.Queue.AI.Summary.ReadmeMaxBytes) {
		remote, err := e.verifyReadmeRepository(ctx, *job.RepoID, document.SourceRepoName, policy)
		if err == nil {
			return describeReadme(remote, snapshot), false, nil
		}
		if !readmeAddressChanged(err) {
			return source{}, false, err
		}
		etag = ""
	}
	return e.readSource(ctx, *job.RepoID, etag, policy)
}

func readmeFits(document storage.RepoReadmeDocument, limit int) bool {
	return document.ReadmeBytes <= limit && (!document.Truncated || document.ReadmeBytes == limit)
}

// describeReadme 为下载结果和已验证的同版快照生成相同的分析输入。
func describeReadme(remote *sdk.Repository, readme gh.ReadmeResult) source {
	s := source{fullName: remote.GetFullName(), description: strings.Join(strings.Fields(remote.GetDescription()), " "),
		githubTopics: remote.GetTopics(), readme: readme, quality: sufficient}
	if readme.Missing {
		s.fingerprint, s.quality = descriptionKey(s.description), insufficient
	} else {
		s.sha, s.etag, s.url = readme.SHA, readme.ETag, readme.URL
		s.fingerprint = consts.ReadmeFingerprintPrefix + s.sha
		s.body = prose(readme.Body)
		if readme.Truncated {
			s.quality = incomplete
		} else {
			if utf8.RuneCountInString(s.body) < 480 {
				// 很短的 README 通常只够支撑简要介绍，仍可生成有依据的推荐理由。
				s.quality = insufficient
			}
		}
	}
	natural := s.body
	if natural == "" {
		natural = s.description
	}
	s.language = language(natural)
	return s
}
