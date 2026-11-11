package settings

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
)

// DiscoveryTab 使用分类 slug 筛选，空名称表示沿用分类名称。
type DiscoveryTab struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

type DiscoveryTabOption struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type DiscoveryTabSettings struct {
	Revision int64                `json:"revision"`
	Items    []DiscoveryTab       `json:"items"`
	Options  []DiscoveryTabOption `json:"options"`
}

func discoveryCategoryNames(categories []config.Category) map[string]string {
	names := map[string]string{"other": "其他"}
	for _, category := range categories {
		names[category.Slug] = category.Name
	}
	return names
}

// discoveryTabSettings 为已有数据库提供默认标签，首次保存后使用管理员指定的顺序。
func discoveryTabSettings(snapshot Snapshot, categories []config.Category) DiscoveryTabSettings {
	options := make([]DiscoveryTabOption, 0, len(categories)+1)
	for _, category := range categories {
		options = append(options, DiscoveryTabOption{Slug: category.Slug, Name: category.Name})
	}
	options = append(options, DiscoveryTabOption{Slug: "other", Name: "其他"})
	items := snapshot.DiscoveryTabs
	if items == nil {
		items = make([]DiscoveryTab, 0, len(options))
		for _, option := range options {
			items = append(items, DiscoveryTab{Slug: option.Slug})
		}
	}
	return DiscoveryTabSettings{Revision: snapshot.Revision, Items: items, Options: options}
}

func (s Store) DiscoveryTabSettings(ctx context.Context) (DiscoveryTabSettings, error) {
	var result DiscoveryTabSettings
	// 配置与分类使用同一只读快照，避免和采集事务争抢写锁。
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		categories, err := readCategories(tx)
		if err == nil {
			result = discoveryTabSettings(snapshot, categories)
		}
		return err
	}, &sql.TxOptions{ReadOnly: true})
	return result, err
}

// SaveDiscoveryTabs 与分类修改共用修订号，避免保存已被移除的分类。
func (s Store) SaveDiscoveryTabs(ctx context.Context, revision int64, items []DiscoveryTab) (DiscoveryTabSettings, error) {
	var categories []config.Category
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, snapshot *Snapshot) error {
		var err error
		categories, err = readCategories(tx)
		if err != nil {
			return err
		}
		names := discoveryCategoryNames(categories)
		seen := make(map[string]bool, len(items))
		tabs := make([]DiscoveryTab, 0, len(items))
		for _, item := range items {
			item.Label = strings.TrimSpace(item.Label)
			if names[item.Slug] == "" || seen[item.Slug] {
				return &Invalid{Err: fmt.Errorf("发现页标签包含无效或重复分类 %q", item.Slug)}
			}
			if utf8.RuneCountInString(item.Label) > 24 {
				return &Invalid{Err: fmt.Errorf("分类 %q 的标签名称最多 24 个字符", item.Slug)}
			}
			seen[item.Slug] = true
			tabs = append(tabs, item)
		}
		snapshot.DiscoveryTabs = tabs
		return nil
	})
	if err != nil {
		return DiscoveryTabSettings{}, err
	}
	return discoveryTabSettings(snapshot, categories), nil
}
