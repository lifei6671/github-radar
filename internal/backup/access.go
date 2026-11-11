package backup

import (
	"context"
	"crypto/rand"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (m *Manager) Save(ctx context.Context, input SettingsUpdate) (SettingsView, error) {
	m.mu.Lock()
	if err := m.checkIdleLocked(); err != nil {
		m.mu.Unlock()
		return SettingsView{}, err
	}
	if !m.state.initialized {
		m.mu.Unlock()
		return SettingsView{}, ErrSetupRequired
	}
	current := m.state.settings
	m.busy = consts.OperationSettings
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.busy = ""; m.mu.Unlock(); m.wakeup() }()
	next := Settings{Enabled: input.Enabled, Schedule: input.Schedule, RetentionDays: input.RetentionDays, R2: current.R2}
	if input.R2 != nil {
		connection, err := NormalizeConnection(*input.R2)
		if err != nil {
			return SettingsView{}, err
		}
		next.R2 = connection
	}
	next, err := normalizeSettings(next)
	if err != nil {
		return SettingsView{}, err
	}
	if input.R2 != nil || (!current.Enabled && next.Enabled) {
		if err := m.probe(ctx, next.R2); err != nil {
			return SettingsView{}, err
		}
	}
	changed := current.R2.Endpoint != next.R2.Endpoint || current.R2.Bucket != next.R2.Bucket || current.R2.Prefix != next.R2.Prefix
	if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if changed {
			if err := tx.Delete(&storage.MaintenanceState{}, storage.MaintenanceStateColumns.Key+" IN ?", []string{lastBackupKey, backupDayKey, cleanupDayKey}).Error; err != nil {
				return ErrDatabase
			}
		}
		return writeSettings(tx, next)
	}); err != nil {
		return SettingsView{}, err
	}
	m.mu.Lock()
	// 单行数据库提交后，在同一个协调器中替换计划并唤醒调度。
	if changed {
		m.state.lastBackup = nil
		m.state.backupDay, m.state.cleanupDay = "", ""
	}
	m.state.settings = next
	view := m.state.view(time.Now())
	m.mu.Unlock()
	return view, nil
}

func (m *Manager) probe(ctx context.Context, c Connection) error {
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return testConnection(check, m.store(c), c)
}

func (m *Manager) Test(ctx context.Context, c Connection, setup bool) error {
	m.mu.Lock()
	err := m.checkIdleLocked()
	ready := m.state.initialized
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if setup && ready {
		return ErrAlreadyInitialized
	}
	if !setup && !ready {
		return ErrSetupRequired
	}
	normalized, err := NormalizeConnection(c)
	if err != nil {
		return err
	}
	return m.probe(ctx, normalized)
}

func (m *Manager) CreateSession(ctx context.Context, c Connection) (string, time.Time, error) {
	if err := m.Test(ctx, c, true); err != nil {
		return "", time.Time{}, err
	}
	c, err := NormalizeConnection(c)
	if err != nil {
		return "", time.Time{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkIdleLocked(); err != nil {
		return "", time.Time{}, err
	}
	if m.state.initialized {
		return "", time.Time{}, ErrAlreadyInitialized
	}
	now := time.Now()
	for id, session := range m.sessions {
		if !now.Before(session.expires) {
			delete(m.sessions, id)
		}
	}
	id, expires := rand.Text(), now.Add(30*time.Minute).UTC()
	m.sessions[id] = setupSession{c, expires}
	return id, expires, nil
}

func (m *Manager) setupConnectionLocked(id string) (Connection, error) {
	if m.state.initialized {
		return Connection{}, ErrAlreadyInitialized
	}
	session, ok := m.sessions[id]
	if !ok || !time.Now().Before(session.expires) {
		delete(m.sessions, id)
		return Connection{}, ErrSession
	}
	return session.connection, nil
}

func (m *Manager) List(ctx context.Context, sessionID string, setup bool, limit int, cursor string) (ObjectPage, error) {
	m.mu.Lock()
	c := m.state.settings.R2
	var err error
	if setup {
		c, err = m.setupConnectionLocked(sessionID)
	} else if !m.state.initialized {
		err = ErrSetupRequired
	}
	m.mu.Unlock()
	if err != nil {
		return ObjectPage{}, err
	}
	if !c.configured() {
		return ObjectPage{}, ErrNotConfigured
	}
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return listObjects(bounded, m.store(c), c, limit, cursor)
}

func (m *Manager) ResumeAI(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy == consts.OperationRestore || m.blocked {
		return ErrRestoring
	}
	if !m.state.initialized {
		return ErrSetupRequired
	}
	if err := saveSetting(m.db.WithContext(ctx), storage.AutoAIResumeKey, "false"); err != nil {
		return err
	}
	m.state.aiPaused = false
	return nil
}
