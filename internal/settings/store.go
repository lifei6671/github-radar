package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const runtimeKey = "runtime.settings.v1"

var ErrRevision = errors.New("配置已被其他操作修改，请重新读取")
var ErrCapacity = errors.New("监控容量小于已有活跃关注仓库数")

// Invalid 表示客户端提交的设置不符合运行约束。
type Invalid struct{ Err error }

func (e *Invalid) Error() string { return e.Err.Error() }

// Snapshot 以统一修订号协调后台的整表替换，避免覆盖其他管理员的修改。
type Snapshot struct {
	Revision           int64              `json:"revision"`
	Settings           config.Runtime     `json:"settings"`
	DiscoveryTabs      []DiscoveryTab     `json:"discoveryTabs"` // nil 跟随分类映射，空数组只显示「全部」。
	DiscoverySelection DiscoverySelection `json:"discoverySelection"`
}

type Store struct{ DB *gorm.DB }

func read(tx *gorm.DB) (Snapshot, error) {
	var row storage.SystemSetting
	if err := tx.Take(&row, storage.SystemSettingColumns.Key+" = ?", runtimeKey).Error; err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	// 旧版已保存的快照没有指标与维护字段，读取时只补齐新增默认值。
	snapshot.Settings.Metrics = config.DefaultMetrics()
	snapshot.Settings.Maintenance = config.DefaultMaintenance()
	snapshot.Settings.AI.Summary = config.DefaultSummary()
	snapshot.Settings.Embedding = config.DefaultEmbedding()
	snapshot.Settings.Recommendations = config.DefaultRecommendations()
	snapshot.DiscoverySelection = defaultDiscoverySelection()
	if err := json.Unmarshal([]byte(row.ValueJSON), &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("解析数据库运行设置: %w", err)
	}
	snapshot.Settings.DefaultMetricsInterval()
	if snapshot.Revision < 1 {
		return Snapshot{}, errors.New("数据库运行设置缺少有效修订号")
	}
	return snapshot, nil
}

func write(tx *gorm.DB, snapshot Snapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	row := storage.SystemSetting{Key: runtimeKey, ValueJSON: string(data)}
	return tx.Save(&row).Error
}

// Initialize 仅在没有持久化设置时导入 YAML。运行设置、分类和初始规则一起提交。
func (s Store) Initialize(ctx context.Context, seed config.Config) (Snapshot, error) {
	var snapshot Snapshot
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = read(tx)
		if err == nil {
			return snapshot.Settings.Validate()
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		seed, err := seed.SeedConfig()
		if err != nil {
			return err
		}
		values := seed.SeedRuntime()
		if err := values.Validate(); err != nil {
			return err
		}
		if err := checkCapacity(tx, values); err != nil {
			return err
		}
		categories, err := config.LoadCategories(seed.CategoriesFile)
		if err != nil {
			return err
		}
		if err := insertCategories(tx, categories); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&storage.DiscoveryRule{}).Count(&count).Error; err != nil {
			return err
		}
		// 旧版本已持久化的规则、ID 和下次运行时间优先保留。
		if count == 0 {
			rules, err := initialRules(seed, time.Now())
			if err != nil {
				return err
			}
			if err := tx.Create(&rules).Error; err != nil {
				return err
			}
		}
		snapshot = Snapshot{Revision: 1, Settings: values, DiscoverySelection: defaultDiscoverySelection()}
		return write(tx, snapshot)
	})
	return snapshot, err
}

func (s Store) Get(ctx context.Context) (Snapshot, error) {
	return read(s.DB.WithContext(ctx))
}

// SaveRuntime 保存下一次启动使用的进程级设置。
func (s Store) SaveRuntime(ctx context.Context, revision int64, values config.Runtime) (Snapshot, error) {
	if err := values.Validate(); err != nil {
		return Snapshot{}, &Invalid{Err: err}
	}
	return s.update(ctx, revision, func(tx *gorm.DB, snapshot *Snapshot) error {
		if err := checkCapacity(tx, values); err != nil {
			return err
		}
		snapshot.Settings = values
		return nil
	})
}

func checkCapacity(tx *gorm.DB, values config.Runtime) error {
	var followed int64
	if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "' AND " + storage.RepositoryColumns.StarredByUser + "=1").Count(&followed).Error; err != nil {
		return err
	}
	if followed > int64(values.Scheduler.MaxMonitoredRepos) {
		return ErrCapacity
	}
	return nil
}

func (s Store) update(ctx context.Context, revision int64, apply func(*gorm.DB, *Snapshot) error) (Snapshot, error) {
	var snapshot Snapshot
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = read(tx)
		if err != nil {
			return err
		}
		if revision != snapshot.Revision {
			return ErrRevision
		}
		if err := apply(tx, &snapshot); err != nil {
			return err
		}
		snapshot.Revision++
		return write(tx, snapshot)
	})
	return snapshot, err
}
