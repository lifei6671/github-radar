package backup

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func (m *Manager) StartRestore(key string, newConnection *Connection, sessionID string, setup bool) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkIdleLocked(); err != nil {
		return Operation{}, err
	}
	cfg := m.state.settings
	if setup {
		connection, err := m.setupConnectionLocked(sessionID)
		if err != nil {
			return Operation{}, err
		}
		cfg.R2 = connection
	} else {
		if !m.state.initialized {
			return Operation{}, ErrSetupRequired
		}
		if newConnection != nil {
			connection, err := NormalizeConnection(*newConnection)
			if err != nil {
				return Operation{}, err
			}
			cfg.R2 = connection
		}
	}
	if !cfg.R2.configured() {
		return Operation{}, ErrNotConfigured
	}
	if _, ok := objectDate(cfg.R2.Prefix, key); !ok {
		return Operation{}, ErrInvalidObject
	}
	op := m.newOperationLocked(consts.OperationRestore)
	m.launchLocked(op, func() error { return m.restore(op.ID, key, cfg, setup, !setup && newConnection != nil) })
	return op, nil
}

func (m *Manager) restore(id, key string, cfg Settings, setup, probe bool) error {
	dir, err := m.stagingDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	defer m.measure(id, dir)
	prep, cancel := context.WithTimeout(m.root, 10*time.Minute)
	if probe {
		if err := m.probe(prep, cfg.R2); err != nil {
			cancel()
			return err
		}
	}
	path, err := m.prepareRestore(prep, id, key, dir, cfg, setup)
	cancel()
	if err != nil {
		return err
	}
	m.measure(id, dir)

	m.update(id, func(op *Operation) { op.Phase = consts.PhaseDrain })
	started := time.Now()
	defer func() { m.update(id, func(op *Operation) { op.MaintenanceMS = time.Since(started).Milliseconds() }) }()
	drain, cancel := context.WithTimeout(m.root, 30*time.Second)
	err = m.drain(drain)
	cancel()
	if err != nil {
		m.openBusiness(nil)
		return Fault(consts.CodeRestoreDrainFailed)
	}

	m.update(id, func(op *Operation) { op.Phase = consts.PhaseCopy })
	copyCtx, cancel := context.WithTimeout(m.root, 5*time.Minute)
	if err := m.preserveBudget(copyCtx, path); err != nil {
		cancel()
		m.openBusiness(nil)
		return err
	}
	walPath := m.cfg.Storage.Path + "-wal"
	wal := fileSize(walPath)
	copyErr := nativeRestore(copyCtx, m.pool, path, func() { wal = max(wal, fileSize(walPath)) })
	cancel()
	m.update(id, func(op *Operation) { op.WALBytes = max(op.WALBytes, wal, fileSize(walPath)) })

	// 复制报错时仍读取持久凭证，以 SQLite 的提交结果为准。
	verify, cancel := context.WithTimeout(context.WithoutCancel(m.root), 15*time.Second)
	defer cancel()
	result, err := readReceipt(verify, m.db)
	if err != nil {
		return Fault(consts.CodeRestoreDatabaseUnavailable)
	}
	if result.OperationID == id {
		m.update(id, func(op *Operation) {
			op.Phase = consts.PhaseReload
			op.DatabaseApplied = true
			op.PreparedAt = &result.PreparedAt
		})
		return m.reload(verify, id)
	}
	if copyErr == nil {
		return Fault(consts.CodeRestoreReceiptMissing)
	}
	state, err := m.readLive(verify)
	if err != nil {
		return Fault(consts.CodeRestoreDatabaseUnavailable)
	}
	m.openBusiness(&state)
	return Fault(consts.CodeRestoreCopyFailed)
}

// preserveBudget 在业务排空后合并近期发送与未决请求。恢复旧备份不能重新开放已消耗的当日额度。
func (m *Manager) preserveBudget(ctx context.Context, path string) error {
	var rows []storage.AIBudgetReservation
	cutoff := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour).UnixMilli()
	if err := m.db.WithContext(ctx).Where("sending_at >= ? OR status IN ?", cutoff,
		[]string{consts.StatusSending, consts.StatusUnknown}).Find(&rows).Error; err != nil {
		return ErrDatabase
	}
	if len(rows) == 0 {
		return nil
	}
	pool, err := storage.OpenBootstrap(ctx, path)
	if err != nil {
		return ErrDatabase
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return ErrDatabase
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			if rows[i].Status == consts.StatusSending {
				rows[i].Status = consts.StatusUnknown
			}
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "reservation_key"}}, UpdateAll: true,
		}).CreateInBatches(&rows, 100).Error
	}); err != nil {
		return ErrDatabase
	}
	return nil
}

// prepareRestore 只修改 staging 中的数据库，验证完成后关闭它的连接池。
func (m *Manager) prepareRestore(ctx context.Context, id, key, dir string, cfg Settings, setup bool) (string, error) {
	path, obj, err := download(ctx, m.store(cfg.R2), cfg.R2, key, dir)
	if err != nil {
		return "", err
	}
	pool, err := storage.OpenBootstrap(ctx, path)
	if err != nil {
		return "", ErrDatabase
	}
	defer pool.Close()
	if err := checkSQLite(ctx, pool); err != nil {
		return "", err
	}
	version, err := migrations.BackupVersion(ctx, pool)
	if err != nil || version != obj.SchemaVersion {
		return "", Fault(consts.CodeBackupSchemaUnsupported)
	}
	sourcePage, err := pageSize(ctx, pool)
	if err != nil {
		return "", err
	}
	targetPage, err := pageSize(ctx, m.pool)
	if err != nil {
		return "", err
	}
	var mode string
	if err := m.pool.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", ErrDatabase
	}
	if mode == "wal" && sourcePage != targetPage {
		return "", Fault(consts.CodeRestorePageSizeMismatch)
	}
	if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil || mode != "delete" {
		return "", ErrDatabase
	}
	if _, err := migrations.Up(ctx, pool); err != nil {
		return "", Fault(consts.CodeBackupMigrationFailed)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return "", ErrDatabase
	}
	store := settings.Store{DB: db}
	if _, err := store.Initialize(ctx, m.cfg); err != nil {
		return "", ErrDatabase
	}
	if err := store.InstallEarlyDiscovery(ctx, time.Now()); err != nil {
		return "", ErrDatabase
	}
	if setup {
		var row storage.SystemSetting
		if err := db.WithContext(ctx).Find(&row, storage.SystemSettingColumns.Key+"=?", settingsKey).Error; err != nil {
			return "", ErrDatabase
		}
		var saved storedSettings
		if json.Unmarshal([]byte(row.ValueJSON), &saved) == nil && saved.Version == 1 && validatePlan(saved.Schedule, saved.RetentionDays) == nil {
			cfg.Schedule, cfg.RetentionDays, cfg.Enabled = saved.Schedule, saved.RetentionDays, saved.Enabled
		}
	}

	at := time.Now().UTC()
	mark := receipt{OperationID: id, PreparedAt: at}
	data, err := json.Marshal(mark)
	if err != nil {
		return "", ErrDatabase
	}
	backupData, err := json.Marshal(obj)
	if err != nil {
		return "", ErrDatabase
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := writeSettings(tx, cfg); err != nil {
			return err
		}
		if err := saveSetting(tx, storage.InitializedKey, "true"); err != nil {
			return err
		}
		if err := saveSetting(tx, storage.AutoAIResumeKey, "true"); err != nil {
			return err
		}
		if err := resetWork(tx, at.UnixMilli()); err != nil {
			return err
		}
		if err := tx.Delete(&storage.MaintenanceState{}, storage.MaintenanceStateColumns.Key+" IN ?", []string{lastBackupKey, backupDayKey, cleanupDayKey}).Error; err != nil {
			return ErrDatabase
		}
		if err := setMark(tx, lastBackupKey, string(backupData)); err != nil {
			return err
		}
		return setMark(tx, receiptKey, string(data))
	}); err != nil {
		return "", err
	}

	if err := checkSQLite(ctx, pool); err != nil {
		return "", err
	}
	stored, err := readSettings(ctx, db)
	if err != nil || stored != cfg {
		return "", ErrDatabase
	}
	initialized, err := storage.Initialized(ctx, db)
	if err != nil || !initialized {
		return "", ErrDatabase
	}
	paused, err := storage.BooleanSetting(ctx, db, storage.AutoAIResumeKey)
	if err != nil || !paused {
		return "", ErrDatabase
	}
	saved, err := readReceipt(ctx, db)
	if err != nil || saved.OperationID != id {
		return "", ErrDatabase
	}
	if err := pool.Close(); err != nil {
		return "", ErrDatabase
	}
	return path, nil
}

func resetWork(tx *gorm.DB, at int64) error {
	if err := tx.Model(&storage.AIBudgetReservation{}).Where("status = ?", consts.StatusSending).
		Updates(map[string]any{"status": consts.StatusUnknown, "settled_at": at}).Error; err != nil {
		return ErrDatabase
	}
	aiKinds := []string{consts.JobRefreshSummary, consts.JobTranslateReadme, consts.JobComposeReport}
	uncertain := tx.Model(&storage.AICall{}).Select("job_id").Where("status IN ?", []string{consts.StatusSending, consts.StatusUnknown})
	if err := tx.Model(&storage.Job{}).Where("type IN ? AND (status = ? OR id IN (?))", aiKinds, consts.StatusUnknown, uncertain).
		Updates(map[string]any{"status": consts.StatusUnknown, "last_error_code": consts.CodeAIDeliveryUnknown,
			"lease_token": nil, "lease_until": nil, "updated_at": at}).Error; err != nil {
		return ErrDatabase
	}
	if err := tx.Model(&storage.Job{}).Where(storage.JobColumns.Status+" IN ?", []string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed, consts.StatusUnknown}).
		Where("type NOT IN ? OR status <> ?", aiKinds, consts.StatusUnknown).
		Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LastErrorCode: consts.CodeRestoreReset, storage.JobColumns.LeaseToken: nil, storage.JobColumns.LeaseUntil: nil, storage.JobColumns.UpdatedAt: at}).Error; err != nil {
		return ErrDatabase
	}
	if err := tx.Model(&storage.DiscoveryRuleRun{}).Where("finished_at = 0").
		Updates(map[string]any{"status": consts.StatusCanceled, "finished_at": at, "error_code": consts.CodeRestoreReset}).Error; err != nil {
		return ErrDatabase
	}
	aiJobs := tx.Model(&storage.Job{}).Select("id").Where("type IN ? AND status = ?", aiKinds, consts.StatusUnknown)
	if err := tx.Model(&storage.AICall{}).Where("status = ? AND job_id IN (?)", consts.StatusSending, aiJobs).
		Updates(map[string]any{"status": consts.StatusUnknown, "error_code": consts.CodeAIDeliveryUnknown, "updated_at": at}).Error; err != nil {
		return ErrDatabase
	}
	if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.Status+" IN ?", []string{consts.StatusReserved, consts.StatusSending, consts.StatusUnknown}).
		Where("status = ? OR job_id NOT IN (?)", consts.StatusReserved, aiJobs).
		Updates(map[string]any{storage.AICallColumns.Status: consts.StatusFailed, storage.AICallColumns.ErrorCode: consts.CodeRestoreResetUnknown, storage.AICallColumns.UpdatedAt: at}).Error; err != nil {
		return ErrDatabase
	}
	if err := tx.Where("1=1").Delete(&storage.RateLimit{}).Error; err != nil {
		return ErrDatabase
	}
	for _, update := range []struct {
		model  any
		column string
	}{
		{&storage.Repository{}, storage.RepositoryColumns.NextFetchAt},
		{&storage.DiscoveryRule{}, storage.DiscoveryRuleColumns.NextRunAt},
		{&storage.SummaryCheck{}, storage.SummaryCheckColumns.NextCheckAt},
	} {
		if err := tx.Model(update.model).Where("1=1").UpdateColumn(update.column, at).Error; err != nil {
			return ErrDatabase
		}
	}
	return nil
}

// drain 先关闭新请求入口，再等 Worker、活跃请求和借出的连接结束。
func (m *Manager) drain(ctx context.Context) error {
	m.mu.Lock()
	m.blocked = true
	idle := m.idle
	m.mu.Unlock()
	if m.workers != nil {
		if err := m.workers.Pause(ctx); err != nil {
			return err
		}
	}
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var running int64
	if err := m.db.WithContext(ctx).Model(&storage.Job{}).Where(storage.JobColumns.Status+"=? AND "+storage.JobColumns.LeaseUntil+">?", consts.StatusRunning, time.Now().UnixMilli()).Count(&running).Error; err != nil {
		return err
	}
	if running != 0 || m.pool.Stats().InUse != 0 {
		return Fault(consts.CodeRestoreDrainFailed)
	}
	return ctx.Err()
}

func (m *Manager) openBusiness(state *liveState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state != nil {
		m.state = *state
		if state.initialized {
			clear(m.sessions)
		}
	}
	m.blocked = false
	m.failedReload = ""
	if m.state.initialized && m.workers != nil {
		m.workers.Resume()
	}
	m.wakeup()
}

func (m *Manager) reload(ctx context.Context, id string) error {
	state, err := m.readLive(ctx)
	valid := err == nil && state.receipt.OperationID == id && state.initialized
	if valid && m.runtime != nil {
		err = m.runtime.RestoreRuntime(state.runtime)
	}
	if !valid || err != nil {
		m.mu.Lock()
		m.failedReload = id
		m.mu.Unlock()
		return Fault(consts.CodeRestoreReloadFailed)
	}
	m.openBusiness(&state)
	return nil
}

// Reload 仅重试已提交恢复的加载阶段。
func (m *Manager) Reload() (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy != "" {
		return Operation{}, ErrBusy
	}
	id := m.failedReload
	if !m.blocked || id == "" {
		return Operation{}, ErrReload
	}
	op := m.operations[id]
	op.Status, op.ErrorCode, op.UpdatedAt = consts.StatusRunning, "", time.Now().UTC()
	m.operations[id] = op
	m.busy = consts.OperationRestore
	m.launchLocked(op, func() error {
		ctx, cancel := context.WithTimeout(m.root, 15*time.Second)
		defer cancel()
		return m.reload(ctx, id)
	})
	return op, nil
}
