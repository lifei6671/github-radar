package settings

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const earlyDiscoverySeedKey = "discovery.early.seed.v1"

// InstallEarlyDiscovery 为新库及升级库安装一次默认规则，保留管理员已使用的同名规则和后续删除操作。
func (s Store) InstallEarlyDiscovery(ctx context.Context, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var marker storage.SystemSetting
		if err := tx.Find(&marker, "key = ?", earlyDiscoverySeedKey).Error; err != nil || marker.Key != "" {
			return err
		}
		var lastPosition int
		if err := tx.Model(&storage.DiscoveryRule{}).Select("COALESCE(MAX(display_position), -1)").Scan(&lastPosition).Error; err != nil {
			return err
		}
		defaults := []storage.DiscoveryRule{
			{SourceKey: "early-global-0-3", AgeWindow: "0-3", Query: "archived:false", MinStars: 10, IntervalMinutes: 120, Priority: 65},
			{SourceKey: "early-global-4-14", AgeWindow: "4-14", Query: "archived:false", MinStars: 50, IntervalMinutes: 240, Priority: 60},
			{SourceKey: "early-global-15-30", AgeWindow: "15-30", Query: "archived:false", MinStars: 150, IntervalMinutes: 720, Priority: 55},
			{SourceKey: "early-topic-mcp", AgeWindow: "0-3", Query: "topic:mcp archived:false", MinStars: 10, IntervalMinutes: 120, Priority: 63},
			{SourceKey: "early-keyword-coding-agent", AgeWindow: "4-14", Query: "\"coding agent\" in:name,description archived:false", MinStars: 50, IntervalMinutes: 240, Priority: 58},
		}
		var inserted int64
		for i := range defaults {
			rule := &defaults[i]
			rule.SourceType, rule.Enabled = consts.SourceSearch, true
			rule.NextRunAt = now.Add(time.Duration(i) * time.Minute).UnixMilli()
			rule.DisplayPosition = lastPosition + i + 1
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_type"}, {Name: "source_key"}}, DoNothing: true}).Create(rule)
			if result.Error != nil {
				return result.Error
			}
			inserted += result.RowsAffected
		}
		if inserted > 0 {
			// 升级前打开的整表编辑器必须重新加载，避免无意删除新增规则。
			snapshot, err := read(tx)
			if err != nil {
				return err
			}
			snapshot.Revision++
			if err := write(tx, snapshot); err != nil {
				return err
			}
		}
		return tx.Create(&storage.SystemSetting{Key: earlyDiscoverySeedKey, ValueJSON: "true", UpdatedAt: now.UnixMilli()}).Error
	})
}
