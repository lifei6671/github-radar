package settings

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
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
	Revision  int64                `json:"revision"`
	Items     []DiscoveryTab       `json:"items"`
	Options   []DiscoveryTabOption `json:"options"`
	Selection DiscoverySelection   `json:"selection"`
	Ranking   DiscoveryRanking     `json:"ranking"`
}

// DiscoverySelection 保存发现页的滚动时间窗口和全站日榜名次门槛。
type DiscoverySelection struct {
	RecentDays        int `json:"recentDays"`
	RecentSampleHours int `json:"recentSampleHours"`
	GrowthSampleHours int `json:"growthSampleHours"`
	TrendingHours     int `json:"trendingHours"`
	TrendingTop       int `json:"trendingTop"`
}

// DiscoveryRanking 控制发现页的展示分和老项目轮换。
type DiscoveryRanking struct {
	HeatWeight      int `json:"heatWeight"`
	FreshnessWeight int `json:"freshnessWeight"`
	ConsecutiveDays int `json:"consecutiveDays"`
	CooldownDays    int `json:"cooldownDays"`
}

func defaultDiscoveryRanking() DiscoveryRanking {
	return DiscoveryRanking{HeatWeight: 70, FreshnessWeight: 30, ConsecutiveDays: 3, CooldownDays: 2}
}

func (r DiscoveryRanking) validate() error {
	if r.HeatWeight < 0 || r.HeatWeight > 100 || r.FreshnessWeight != 100-r.HeatWeight {
		return fmt.Errorf("热度与新鲜度权重必须在 0%% 到 100%% 之间，合计为 100%%")
	}
	maxDays := config.DurationLimit(24 * time.Hour)
	if r.ConsecutiveDays < 1 || int64(r.ConsecutiveDays) > maxDays || r.CooldownDays < 0 || int64(r.CooldownDays) > maxDays {
		return fmt.Errorf("连续上榜天数须为正整数，冷却天数须为非负整数，两者最多 %d 天", maxDays)
	}
	return nil
}

func defaultDiscoverySelection() DiscoverySelection {
	return DiscoverySelection{RecentDays: 7, RecentSampleHours: 48, GrowthSampleHours: 24, TrendingHours: 24, TrendingTop: 10}
}

func (s DiscoverySelection) validate() error {
	if s.RecentDays < 1 || int64(s.RecentDays) > config.DurationLimit(24*time.Hour) || s.TrendingTop < 1 {
		return fmt.Errorf("发现天数和上榜名次须为有效的正整数")
	}
	for _, hours := range []int{s.RecentSampleHours, s.GrowthSampleHours, s.TrendingHours} {
		if hours < 1 || int64(hours) > config.DurationLimit(time.Hour) {
			return fmt.Errorf("发现页的采样和上榜窗口须为有效的正整数小时")
		}
	}
	return nil
}

func discoveryCategoryNames(categories []config.Category) map[string]string {
	names := map[string]string{consts.CategoryOther: "其他"}
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
	options = append(options, DiscoveryTabOption{Slug: consts.CategoryOther, Name: "其他"})
	items := snapshot.DiscoveryTabs
	if items == nil {
		items = make([]DiscoveryTab, 0, len(options))
		for _, option := range options {
			items = append(items, DiscoveryTab{Slug: option.Slug})
		}
	}
	return DiscoveryTabSettings{Revision: snapshot.Revision, Items: items, Options: options, Selection: snapshot.DiscoverySelection,
		Ranking: snapshot.DiscoveryRanking}
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
func (s Store) SaveDiscoveryTabs(ctx context.Context, revision int64, items []DiscoveryTab, newCategories []config.Category, selection *DiscoverySelection, ranking *DiscoveryRanking) (DiscoveryTabSettings, error) {
	if selection != nil {
		if err := selection.validate(); err != nil {
			return DiscoveryTabSettings{}, &Invalid{Err: err}
		}
	}
	if ranking != nil {
		if err := ranking.validate(); err != nil {
			return DiscoveryTabSettings{}, &Invalid{Err: err}
		}
	}
	var categories []config.Category
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, snapshot *Snapshot) error {
		var err error
		categories, err = readCategories(tx)
		if err != nil {
			return err
		}
		// 从标签页新建的分类和标签共用一次保存，任一校验失败都不会留下半份配置。
		if len(newCategories) > 0 {
			count := len(categories)
			categories, err = config.NormalizeCategories(append(categories, newCategories...))
			if err != nil {
				return &Invalid{Err: err}
			}
			if err := insertCategoriesAt(tx, categories[count:], count); err != nil {
				return err
			}
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
		if selection != nil {
			snapshot.DiscoverySelection = *selection
		}
		if ranking != nil {
			snapshot.DiscoveryRanking = *ranking
		}
		return nil
	})
	if err != nil {
		return DiscoveryTabSettings{}, err
	}
	return discoveryTabSettings(snapshot, categories), nil
}
