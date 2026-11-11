package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

func setMark(db *gorm.DB, key, value string) error {
	row := storage.MaintenanceState{Key: key, Value: value, UpdatedAt: time.Now().UnixMilli()}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return ErrDatabase
	}
	return nil
}

func (m *Manager) StartBackup() (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkIdleLocked(); err != nil {
		return Operation{}, err
	}
	if !m.state.initialized {
		return Operation{}, ErrSetupRequired
	}
	if !m.state.settings.R2.configured() {
		return Operation{}, ErrNotConfigured
	}
	return m.startBackupLocked(m.state.settings, time.Now()), nil
}

func (m *Manager) startBackupLocked(cfg Settings, now time.Time) Operation {
	op := m.newOperationLocked(consts.OperationBackup)
	m.launchLocked(op, func() error { return m.backup(op.ID, cfg, now) })
	return op
}

func (m *Manager) backup(id string, cfg Settings, now time.Time) error {
	ctx, cancel := context.WithTimeout(m.root, 10*time.Minute)
	defer cancel()
	dir, err := m.stagingDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	defer m.measure(id, dir)
	var pages int64
	if err := m.pool.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return ErrDatabase
	}
	page, err := pageSize(ctx, m.pool)
	if err != nil {
		return err
	}
	estimate := pages * page
	if err := ensureSpace(dir, estimate, estimate, estimate/8); err != nil {
		return err
	}
	rawPath, gzPath := filepath.Join(dir, "snapshot.db"), filepath.Join(dir, "snapshot.db.gz")
	version, err := snapshot(ctx, m.pool, rawPath)
	if err != nil {
		return err
	}
	raw, size, sha, md5, err := compressFile(ctx, rawPath, gzPath)
	if err != nil {
		return err
	}
	date := now.In(cfg.location()).Format(time.DateOnly)
	generated := time.Now().UTC()
	obj := Object{Date: date, RawSize: raw, Size: size, SchemaVersion: version, GeneratedAt: &generated, SHA256: sha}
	store := m.store(cfg.R2)
	m.update(id, func(op *Operation) { op.Phase = consts.PhaseCopy })
	saved, err := upload(ctx, store, cfg.R2.Prefix+date+objectSuffix, date, gzPath, obj, md5)
	if err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return ErrDatabase
	}
	if err := setMark(m.db.WithContext(ctx), lastBackupKey, string(data)); err != nil {
		return err
	}
	m.mu.Lock()
	m.state.lastBackup = &saved
	m.mu.Unlock()
	// 键名与上传后的保留窗口使用同一日期，跨午夜也保留刚生成的对象。
	if err := cleanObjects(ctx, store, cfg, now); err != nil {
		m.update(id, func(op *Operation) { op.Warning = consts.CodeBackupCleanupFailed })
	}
	return nil
}

// Run 每日记录一次自动尝试。失败保留在操作历史中，可立即手动补跑。
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		m.schedule(ctx, time.Now())
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

func (state liveState) scheduleKeys(local time.Time) (backupDay, cleanupDay string) {
	schedule := state.settings.Schedule
	cleanupDay = schedule.Timezone + "/" + local.Format(time.DateOnly)
	return fmt.Sprintf("%s/%02d:%02d", cleanupDay, schedule.Hour, schedule.Minute), cleanupDay
}

func (state liveState) backupDue(local time.Time, day string) bool {
	schedule := state.settings.Schedule
	return local.Hour()*60+local.Minute() >= schedule.Hour*60+schedule.Minute && state.backupPending(local, day)
}

func (state liveState) backupPending(local time.Time, day string) bool {
	return state.backupDay != day && (state.lastBackup == nil || state.lastBackup.Date != local.Format(time.DateOnly))
}

func (state liveState) view(now time.Time) SettingsView {
	cfg := state.settings
	view := cfg.view()
	if cfg.Enabled {
		local := now.In(cfg.location())
		backupDay, cleanupDay := state.scheduleKeys(local)
		backup := nextTime(local, cfg.Schedule.Hour, cfg.Schedule.Minute).UTC()
		cleanup := nextTime(local, 3, 30).UTC()
		if !state.backupPending(local, backupDay) {
			backup = atDate(local.AddDate(0, 0, 1), cfg.Schedule.Hour, cfg.Schedule.Minute).UTC()
		} else if state.backupDue(local, backupDay) {
			backup = now.UTC()
		}
		if local.Hour()*60+local.Minute() >= 210 && state.cleanupDay != cleanupDay {
			cleanup = now.UTC()
		}
		view.NextBackupAt, view.NextCleanupAt = &backup, &cleanup
	}
	return view
}

func (m *Manager) schedule(ctx context.Context, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.state.settings
	if !cfg.Enabled || !m.state.initialized || m.checkIdleLocked() != nil {
		return
	}
	local := now.In(cfg.location())
	backupDay, day := m.state.scheduleKeys(local)
	if m.state.backupDue(local, backupDay) {
		if setMark(m.db.WithContext(ctx), backupDayKey, backupDay) == nil {
			m.state.backupDay = backupDay
			m.startBackupLocked(cfg, now)
		}
		return
	}
	if local.Hour()*60+local.Minute() >= 3*60+30 && m.state.cleanupDay != day {
		if setMark(m.db.WithContext(ctx), cleanupDayKey, day) != nil {
			return
		}
		m.state.cleanupDay = day
		op := m.newOperationLocked(consts.OperationCleanup)
		m.launchLocked(op, func() error {
			bounded, cancel := context.WithTimeout(m.root, 2*time.Minute)
			defer cancel()
			return cleanObjects(bounded, m.store(cfg.R2), cfg, now)
		})
	}
}

func (m *Manager) measure(id, dir string) {
	staging := stageSize(dir)
	wal := fileSize(m.cfg.Storage.Path + "-wal")
	m.update(id, func(op *Operation) {
		op.StagingBytes = max(op.StagingBytes, staging)
		op.WALBytes = max(op.WALBytes, wal)
	})
}
