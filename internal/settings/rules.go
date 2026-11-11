package settings

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

// Rule 只暴露可编辑定义。调度状态和内部数字 ID 由数据库维护。
type Rule struct {
	SourceType      string `json:"sourceType"`
	SourceKey       string `json:"sourceKey"`
	Query           string `json:"query"`
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Priority        int    `json:"priority"`
}

func initialRules(seed config.Config, now time.Time) ([]storage.DiscoveryRule, error) {
	if err := seed.ValidateDiscoverySeed(); err != nil {
		return nil, err
	}
	var rules []storage.DiscoveryRule
	sources := append([]string{""}, seed.Discovery.TrendingLanguages...)
	for _, language := range sources {
		source := consts.TrendingAll
		if language != "" {
			source = "language:" + language
		}
		for _, period := range []string{consts.PeriodDaily, consts.PeriodWeekly, consts.PeriodMonthly} {
			interval, priority := seed.Scheduler.TrendingOtherMinutes, 10
			if period == consts.PeriodDaily {
				interval, priority = seed.Scheduler.TrendingDailyMinutes, 30
			}
			rules = append(rules, storage.DiscoveryRule{
				SourceType: consts.SourceTrending, SourceKey: source + ":" + period,
				Enabled: seed.Discovery.TrendingEnabled, IntervalMinutes: interval,
				Priority: priority, NextRunAt: now.UnixMilli(),
			})
		}
	}
	enabled := 0
	for _, rule := range seed.Discovery.SearchRules {
		if rule.Enabled {
			enabled++
		}
	}
	round := seed.Scheduler.SearchRoundMinutes
	if int64(round) > config.DurationLimit(time.Minute)/int64(max(enabled, 1)) {
		return nil, fmt.Errorf("首次导入的搜索轮换周期过长")
	}
	offset := 0
	for _, rule := range seed.Discovery.SearchRules {
		rules = append(rules, storage.DiscoveryRule{
			SourceType: consts.SourceSearch, SourceKey: rule.Key, Query: strings.TrimSpace(rule.Query), Enabled: rule.Enabled,
			IntervalMinutes: round * max(enabled, 1), Priority: rule.Priority,
			NextRunAt: now.Add(time.Duration(offset*round) * time.Minute).UnixMilli(),
		})
		if rule.Enabled {
			offset++
		}
	}
	return rules, nil
}

func rulesFromDB(tx *gorm.DB) ([]storage.DiscoveryRule, error) {
	var rows []storage.DiscoveryRule
	err := tx.Order(storage.DiscoveryRuleColumns.DisplayPosition + ", " + storage.DiscoveryRuleColumns.SourceType + ", " + storage.DiscoveryRuleColumns.ID).Find(&rows).Error
	return rows, err
}

func ruleView(row storage.DiscoveryRule) Rule {
	query := row.Query
	if row.SourceType == consts.SourceTrending {
		query = ""
	}
	return Rule{row.SourceType, row.SourceKey, query, row.Enabled, row.IntervalMinutes, row.Priority}
}

func (s Store) DiscoveryRules(ctx context.Context) (int64, []Rule, error) {
	var revision int64
	var items []Rule
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		rows, err := rulesFromDB(tx)
		if err != nil {
			return err
		}
		revision, items = snapshot.Revision, make([]Rule, 0, len(rows))
		for _, row := range rows {
			items = append(items, ruleView(row))
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return revision, items, err
}

type ruleKey struct{ sourceType, sourceKey string }

func normalizeRules(items []Rule) ([]Rule, error) {
	rules := make([]Rule, 0, len(items))
	seen := make(map[ruleKey]bool)
	for _, item := range items {
		key := ruleKey{item.SourceType, item.SourceKey}
		if seen[key] || item.IntervalMinutes < 5 || int64(item.IntervalMinutes) > config.DurationLimit(time.Minute) {
			return nil, fmt.Errorf("发现规则 %q 重复或周期无效", item.SourceKey)
		}
		seen[key] = true
		item.Query = strings.TrimSpace(item.Query)
		switch item.SourceType {
		case consts.SourceTrending:
			if _, _, _, err := config.TrendingSource(item.SourceKey); err != nil {
				return nil, err
			}
			if item.Query != "" {
				return nil, fmt.Errorf("Trending 规则使用来源键和周期，无需搜索查询")
			}
		case consts.SourceSearch:
			if !config.ValidSourceSlug(item.SourceKey) || item.Query == "" {
				return nil, fmt.Errorf("Search 规则需要有效键和查询")
			}
		default:
			return nil, fmt.Errorf("发现规则类型必须为 search 或 trending")
		}
		rules = append(rules, item)
	}
	return rules, nil
}

// SaveDiscoveryRules 更新规则、撤销已失效任务的租约并保留未变规则的计划时间。
func (s Store) SaveDiscoveryRules(ctx context.Context, revision int64, items []Rule) (int64, []Rule, error) {
	rules, err := normalizeRules(items)
	if err != nil {
		return 0, nil, &Invalid{Err: err}
	}
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, _ *Snapshot) error {
		rows, err := rulesFromDB(tx)
		if err != nil {
			return err
		}
		existing := make(map[ruleKey]storage.DiscoveryRule, len(rows))
		for _, row := range rows {
			existing[ruleKey{row.SourceType, row.SourceKey}] = row
		}
		now := time.Now().UnixMilli()
		for position, item := range rules {
			key := ruleKey{item.SourceType, item.SourceKey}
			row, found := existing[key]
			delete(existing, key)
			if !found {
				row = storage.DiscoveryRule{
					SourceType: item.SourceType, SourceKey: item.SourceKey, Query: item.Query,
					Enabled: item.Enabled, IntervalMinutes: item.IntervalMinutes, Priority: item.Priority, NextRunAt: now, DisplayPosition: position,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				continue
			}
			if ruleView(row) == item {
				// 只换展示顺序时保留任务计划、规则 ID 和现有租约。
				if row.DisplayPosition != position {
					if err := tx.Model(&row).Update(storage.DiscoveryRuleColumns.DisplayPosition, position).Error; err != nil {
						return err
					}
				}
				continue
			}
			if err := tx.Model(&row).Updates(map[string]any{
				storage.DiscoveryRuleColumns.Query: item.Query, storage.DiscoveryRuleColumns.Enabled: item.Enabled, storage.DiscoveryRuleColumns.IntervalMinutes: item.IntervalMinutes,
				storage.DiscoveryRuleColumns.Priority: item.Priority, storage.DiscoveryRuleColumns.NextRunAt: now, storage.DiscoveryRuleColumns.DisplayPosition: position,
			}).Error; err != nil {
				return err
			}
			if err := cancelRuleJobs(tx, row.ID, now); err != nil {
				return err
			}
		}
		for _, row := range existing {
			if err := cancelRuleJobs(tx, row.ID, now); err != nil {
				return err
			}
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return snapshot.Revision, rules, err
}

func cancelRuleJobs(tx *gorm.DB, id, now int64) error {
	return tx.Model(&storage.Job{}).
		Where(storage.JobColumns.Type+" IN ('"+consts.JobDiscoverSearch+"','"+consts.JobDiscoverTrending+"') AND "+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusRunning+"','"+consts.StatusFailed+"')").
		Where("CAST(json_extract("+storage.JobColumns.PayloadJSON+",'$.ruleId') AS INTEGER) = ?", id).
		Updates(map[string]any{
			storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LeaseToken: nil, storage.JobColumns.LeaseUntil: nil,
			storage.JobColumns.LastErrorCode: consts.CodeSourceRuleChanged, storage.JobColumns.UpdatedAt: now,
		}).Error
}
