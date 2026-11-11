package settings

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

// Categories 为同一个请求读取一份分类映射，供筛选、计数和展示共同使用。
func (s Store) Categories(ctx context.Context) ([]config.Category, error) {
	return readCategories(s.DB.WithContext(ctx))
}

func readCategories(tx *gorm.DB) ([]config.Category, error) {
	var rows []storage.CategorySetting
	if err := tx.Order("position, slug").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]config.Category, 0, len(rows))
	for _, row := range rows {
		category := config.Category{Slug: row.Slug, Name: row.Name}
		if err := json.Unmarshal([]byte(row.TopicsJSON), &category.AnyTopics); err != nil {
			return nil, err
		}
		items = append(items, category)
	}
	return items, nil
}

func insertCategories(tx *gorm.DB, items []config.Category) error {
	return insertCategoriesAt(tx, items, 0)
}

func insertCategoriesAt(tx *gorm.DB, items []config.Category, start int) error {
	rows := make([]storage.CategorySetting, 0, len(items))
	for position, item := range items {
		data, err := json.Marshal(item.AnyTopics)
		if err != nil {
			return err
		}
		rows = append(rows, storage.CategorySetting{Slug: item.Slug, Name: item.Name, TopicsJSON: string(data), Position: start + position})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.CreateInBatches(&rows, 100).Error
}

func (s Store) CategorySettings(ctx context.Context) (int64, []config.Category, error) {
	var revision int64
	var items []config.Category
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		revision = snapshot.Revision
		items, err = readCategories(tx)
		return err
	})
	return revision, items, err
}

// SaveCategories 原子替换有序分类规则，空列表只保留动态回退分类。
func (s Store) SaveCategories(ctx context.Context, revision int64, items []config.Category) (int64, []config.Category, error) {
	normalized, err := config.NormalizeCategories(items)
	if err != nil {
		return 0, nil, &Invalid{Err: err}
	}
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, snapshot *Snapshot) error {
		// 旧库尚未保存展示范围时，先保留修改前已经在发现页显示的标签。
		if snapshot.DiscoveryTabs == nil {
			previous, err := readCategories(tx)
			if err != nil {
				return err
			}
			snapshot.DiscoveryTabs = discoveryTabSettings(*snapshot, previous).Items
		}
		if err := tx.Where("1 = 1").Delete(&storage.CategorySetting{}).Error; err != nil {
			return err
		}
		// 删除分类时同步移除标签，新增分类由管理员自行选入。
		options := discoveryCategoryNames(normalized)
		tabs := snapshot.DiscoveryTabs[:0]
		for _, tab := range snapshot.DiscoveryTabs {
			if _, ok := options[tab.Slug]; ok {
				tabs = append(tabs, tab)
			}
		}
		snapshot.DiscoveryTabs = tabs
		return insertCategories(tx, normalized)
	})
	return snapshot.Revision, normalized, err
}
