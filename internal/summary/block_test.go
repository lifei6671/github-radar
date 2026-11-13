package summary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestEmbeddingReadmeMissingRechecksRemoteVisibility(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.missing = true
	source.onRead = func() { source.private = true }
	repo := row[storage.Repository](t, e.Queue.DB, storage.RepositoryColumns.RepoID+" = ?", 1)
	readme, err := e.EmbeddingReadme(t.Context(), repo)
	if !errors.Is(err, ErrRepoUnavailable) || readme != "" {
		t.Fatalf("仓库在 README 缺失时转私有，应终止材料读取: %v", err)
	}
	repo = row[storage.Repository](t, e.Queue.DB, storage.RepositoryColumns.RepoID+" = ?", 1)
	if repo.State != consts.StatusUnavailable || repo.FirstNotFoundAt == nil {
		t.Fatal("已确认转私的仓库应撤下")
	}
}

type sourceBlockedDuringLookup struct {
	GitHubSource
	lookup func()
}

func (s sourceBlockedDuringLookup) RepositoryByID(ctx context.Context, id int64, p gh.Policy) (gh.RepositoryResult, error) {
	s.lookup()
	return s.GitHubSource.RepositoryByID(ctx, id, p)
}

func TestReadmeRechecksBlockAfterGitHubIdentityLookup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		blockOn    int
		readBefore int
	}{{"正文之前", 1, 0}, {"发布之前", 2, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			e, source, _, _ := fixture(t)
			db := e.Queue.DB
			lookups := 0
			e.GitHub = sourceBlockedDuringLookup{GitHubSource: source, lookup: func() {
				lookups++
				if lookups == tc.blockOn {
					if err := db.Model(&storage.Repository{}).Where("repo_id=1").Update("blocked", true).Error; err != nil {
						t.Fatal(err)
					}
				}
			}}
			repo := row[storage.Repository](t, db, "repo_id=1")
			view, err := e.readReadme(t.Context(), repo, false)
			if !errors.Is(err, storage.ErrRepositoryBlocked) || len(source.etags) != tc.readBefore ||
				len(view.Body) != 0 {
				t.Fatalf("屏蔽后不能继续读取或发布正文: reads=%d view=%+v err=%v", len(source.etags), view, err)
			}
		})
	}
}

func TestUnblockedAutoTranslationCanResumeCanceledJob(t *testing.T) {
	e, _, _, _ := fixture(t)
	e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
	schedule(t, e)
	step(t, e)
	schedule(t, e)
	db := e.Queue.DB
	job := row[storage.Job](t, db, "repo_id=1 AND type=?", TranslationJob)
	if err := db.Model(&job).Updates(map[string]any{"status": "canceled", "last_error_code": "repo_blocked"}).Error; err != nil {
		t.Fatal(err)
	}
	check := row[storage.SummaryCheck](t, db, "repo_id=1")
	if err := db.Create(&storage.RepoReadmeTranslation{RepoID: 1, ReadmeSHA: *check.LastSeenReadmeSHA,
		ReadmeBytes: 20, ModelKey: e.Queue.translationKey(), PartsJSON: `["已完成首批"]`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Schedule(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	resumed := row[storage.Job](t, db, "id=?", job.ID)
	if resumed.Status != "pending" || resumed.LastErrorCode != nil {
		t.Fatalf("解除屏蔽后应继续原任务: %+v", resumed)
	}
}
