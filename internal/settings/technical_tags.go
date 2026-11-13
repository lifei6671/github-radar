package settings

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

// TechnicalTags 在同一个读事务中返回词表和后台修订号。
func (s Store) TechnicalTags(ctx context.Context) (int64, []storage.TechnicalTag, error) {
	var revision int64
	var tags storage.TechnicalTags
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		revision = snapshot.Revision
		tags, err = storage.ReadTechnicalTags(tx)
		return err
	})
	return revision, tags.Items, err
}

// SaveTechnicalTags 与其他后台设置共用修订号，避免管理员互相覆盖修改。
func (s Store) SaveTechnicalTags(ctx context.Context, revision int64, items []storage.TechnicalTag) (int64, []storage.TechnicalTag, error) {
	tags, err := storage.NewTechnicalTags(items)
	if err != nil {
		return 0, nil, &Invalid{Err: err}
	}
	for _, item := range tags.Items {
		if storage.ReservedTaxonomyID(item.Slug) {
			return 0, nil, &Invalid{Err: fmt.Errorf("%q 是其他维度或旧词表的保留标识", item.Slug)}
		}
	}
	data, err := json.Marshal(tags.Items)
	if err != nil {
		return 0, nil, err
	}
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, _ *Snapshot) error {
		return tx.Save(&storage.SystemSetting{Key: storage.TechnicalTagsKey, ValueJSON: string(data)}).Error
	})
	return snapshot.Revision, tags.Items, err
}
