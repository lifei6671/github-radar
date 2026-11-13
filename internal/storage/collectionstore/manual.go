package collectionstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/storage"
)

func ManualDiscoveryPrefix(requestID string) string { return "manual:repo:" + requestID + ":" }

var ErrSubmissionConflict = errors.New("请求 ID 已用于不同的仓库提交")

func (q Queue) SubmitRepositories(ctx context.Context, requestID string, names []string, sourceURL, note string, now time.Time) (map[string]storage.Job, error) {
	canonical := slices.Clone(names)
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	data, err := json.Marshal(append([]string{sourceURL, note}, canonical...))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(sum[:])
	prefix := ManualDiscoveryPrefix(requestID)
	jobs := make(map[string]storage.Job, len(names))
	err = q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing storage.Job
		if err := tx.Where(storage.JobColumns.DedupKey+" GLOB ?", prefix+"*").Take(&existing).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if existing.ID != 0 {
			var previous collection.Payload
			if err := json.Unmarshal([]byte(existing.PayloadJSON), &previous); err != nil {
				return err
			}
			if previous.SubmissionKey != fingerprint {
				return ErrSubmissionConflict
			}
		}
		for _, name := range names {
			if _, exists := jobs[name]; exists {
				continue
			}
			job, err := q.enqueue(tx, consts.JobDiscoverManual, nil, prefix+name,
				collection.Payload{PlannedAt: now.UnixMilli(), Manual: true, Repository: name, SubmissionKey: fingerprint, SourceURL: sourceURL, Note: note}, 200, now.UnixMilli())
			if err != nil {
				return err
			}
			jobs[name] = job
		}
		return nil
	})
	return jobs, err
}

func (w writer) SaveManualDiscovery(job storage.Job, p collection.Payload, repo *sdk.Repository, started, observed time.Time) error {
	c := storage.RepositoryColumns
	var current storage.Repository
	if err := w.db.Take(&current, c.RepoID+" = ?", repo.GetID()).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if current.Blocked {
		return storage.ErrRepositoryBlocked
	}
	// 迟到的响应仍可记录同一数字 ID 的来源，但不能覆盖请求发出后已确认的仓库状态。
	if current.RepoID == 0 || current.UpdatedAt < started.UnixMilli() {
		var conflict int64
		if err := w.db.Model(&storage.Repository{}).
			Where(c.FullName+" = ? COLLATE NOCASE AND "+c.NameIsCurrent+" = 1 AND "+c.RepoID+" <> ? AND "+c.UpdatedAt+" >= ?", repo.GetFullName(), repo.GetID(), started.UnixMilli()).
			Count(&conflict).Error; err != nil {
			return err
		}
		if conflict > 0 {
			return errors.New("仓库地址刚刚变更，等待重新确认")
		}
		accepted, err := w.queue.Discover(w.db, repo, observed.UnixMilli())
		if err != nil {
			return err
		}
		if !accepted {
			return storage.ErrRepositoryBlocked
		}
	}
	if err := w.db.Exec(`INSERT INTO repo_discovery_events (repo_id, source_type, source_key, source_url, first_observed_at, note)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, source_type, source_key) DO UPDATE SET
			first_observed_at = min(repo_discovery_events.first_observed_at, excluded.first_observed_at),
			note = CASE WHEN repo_discovery_events.note = '' THEN excluded.note ELSE repo_discovery_events.note END`,
		repo.GetID(), consts.SourceManual, p.SourceURL, p.SourceURL, p.PlannedAt, p.Note).Error; err != nil {
		return err
	}
	return w.db.Model(&storage.Job{}).Where(storage.JobColumns.ID+" = ?", job.ID).Update(storage.JobColumns.RepoID, repo.GetID()).Error
}
