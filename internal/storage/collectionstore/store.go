package collectionstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/storage"
)

type writer struct {
	queue Queue
	db    *gorm.DB
}

func (q Queue) Writer(tx *gorm.DB) port.Writer {
	return writer{queue: q, db: tx}
}

func (q Queue) Initialized(ctx context.Context) (bool, error) {
	return storage.Initialized(ctx, q.DB)
}

func (q Queue) IsBusy(err error) bool { return storage.IsBusy(err) }

func (q Queue) Interrupt(ctx context.Context, job storage.Job) error {
	return storage.InterruptJob(ctx, q.DB, job)
}

func (q Queue) Repository(ctx context.Context, id int64) (storage.Repository, error) {
	var repo storage.Repository
	err := q.DB.WithContext(ctx).Take(&repo, storage.RepositoryColumns.RepoID+" = ?", id).Error
	return repo, err
}

func (q Queue) KnownRepository(ctx context.Context, id int64, name string) (storage.Repository, bool, error) {
	var repo storage.Repository
	c := storage.RepositoryColumns
	err := q.DB.WithContext(ctx).Take(&repo, c.RepoID+" = ? OR ("+c.FullName+" = ? COLLATE NOCASE AND "+c.NameIsCurrent+"=1)", id, name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return repo, false, nil
	}
	return repo, err == nil, err
}

func (q Queue) IsBlocked(ctx context.Context, id int64, name string) (bool, error) {
	c := storage.RepositoryColumns
	blocked := q.DB.WithContext(ctx).Model(&storage.Repository{}).Where(c.Blocked + "=1")
	if id > 0 {
		blocked = blocked.Where(c.RepoID+"=?", id)
	} else {
		// Trending 只有名称时，先跳过当前已屏蔽地址，避免额外请求仓库详情。
		blocked = blocked.Where(c.FullName+"=? COLLATE NOCASE AND "+c.NameIsCurrent+"=1", name)
	}
	var count int64
	err := blocked.Count(&count).Error
	return count > 0, err
}
