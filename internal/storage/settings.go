package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var ErrAlreadyInitialized = errors.New("应用已经初始化")

// InitializeEmpty 是阶段 1 启动采集所需的空库安装入口。
// 备份默认关闭；同一事务内写入初始设置与安装标记。
func InitializeEmpty(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initialized, err := Initialized(ctx, tx)
		if err != nil {
			return err
		}
		if initialized {
			return ErrAlreadyInitialized
		}
		now := time.Now().UnixMilli()
		for key, value := range map[string]string{
			"app.initialized":         "true",
			"ai.auto_resume_required": "false",
			"backup.settings.v1":      `{"version":1,"enabled":false,"schedule":{"hour":3,"minute":0,"timezone":"Asia/Shanghai"},"retention_days":5,"r2":{"endpoint":"","bucket":"","prefix":"github-radar/backups/","access_key_id":"","secret_access_key":""}}`,
		} {
			setting := SystemSetting{Key: key, ValueJSON: value, UpdatedAt: now}
			if err := tx.Save(&setting).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SystemSetting 显式映射迁移中的 system_settings 表，时间使用 UTC Unix 毫秒。
type SystemSetting struct {
	Key       string `gorm:"column:key;primaryKey"`
	ValueJSON string `gorm:"column:value_json"`
	UpdatedAt int64  `gorm:"column:updated_at;autoUpdateTime:milli"`
}

// TableName 固定表名，表结构由 SQL 迁移维护。
func (SystemSetting) TableName() string {
	return "system_settings"
}

// CategorySetting 显式映射后台分类规则，position 保留管理员设定的顺序。
type CategorySetting struct {
	Slug       string `gorm:"column:slug;primaryKey"`
	Name       string `gorm:"column:name"`
	TopicsJSON string `gorm:"column:topics_json"`
	Position   int    `gorm:"column:position"`
}

func (CategorySetting) TableName() string { return "categories" }

// Initialized 查询安装标记，空数据库没有记录时处于未初始化状态。
func Initialized(ctx context.Context, db *gorm.DB) (bool, error) {
	var setting SystemSetting
	err := db.WithContext(ctx).Where("key = ?", "app.initialized").Take(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取安装状态: %w", err)
	}

	var initialized *bool
	if err := json.Unmarshal([]byte(setting.ValueJSON), &initialized); err != nil || initialized == nil {
		return false, errors.New("app.initialized 必须是 JSON 布尔值")
	}
	return *initialized, nil
}
