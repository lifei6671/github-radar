package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

const receiptKey = "restore.last_operation"
const lastBackupKey = "backup.last_success"
const backupDayKey = "backup.auto_day"
const cleanupDayKey = "backup.cleanup_day"

type Workers interface {
	Pause(context.Context) error
	Resume()
}

type RuntimeReloader interface {
	RestoreRuntime(config.Runtime) error
	RestartRequired() bool
}

type Operation struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	Phase           string     `json:"phase"`
	DatabaseApplied bool       `json:"databaseApplied"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	PreparedAt      *time.Time `json:"preparedAt,omitempty"`
	StagingBytes    int64      `json:"stagingBytes"`
	MaintenanceMS   int64      `json:"maintenanceMs"`
	WALBytes        int64      `json:"walBytes"`
	Warning         string     `json:"warning,omitempty"`
}

type Status struct {
	Initialized          bool       `json:"initialized"`
	Restoring            bool       `json:"restoring"`
	Maintenance          bool       `json:"maintenance"`
	LastBackup           *Object    `json:"lastBackup"`
	LastBackupOperation  *Operation `json:"lastBackupOperation"`
	LastCleanup          *Operation `json:"lastCleanup"`
	LastRestore          *Operation `json:"lastRestore"`
	NextBackupAt         *time.Time `json:"nextBackupAt"`
	NextCleanupAt        *time.Time `json:"nextCleanupAt"`
	AutoAIResumeRequired bool       `json:"autoAIResumeRequired"`
	RestartRequired      bool       `json:"restartRequired"`
}

type receipt struct {
	OperationID string    `json:"operationId"`
	PreparedAt  time.Time `json:"preparedAt"`
}

type liveState struct {
	settings        Settings
	runtime         config.Runtime
	initialized     bool
	aiPaused        bool
	restartRequired bool
	lastBackup      *Object
	receipt         receipt
	backupDay       string
	cleanupDay      string
}

type setupSession struct {
	connection Connection
	expires    time.Time
}

// Manager 协调一次备份、清理或恢复，并在排空后独占已有 SQLite 连接池。
type Manager struct {
	root          context.Context
	pool          *sql.DB
	db            *gorm.DB
	cfg           config.Config
	workers       Workers
	runtime       RuntimeReloader
	store         factory
	staging       string
	mu            sync.Mutex
	state         liveState
	busy          string
	blocked       bool
	failedReload  string
	active        int
	idle          chan struct{}
	operations    map[string]Operation
	order         []string
	lastBackupID  string
	lastCleanupID string
	lastRestoreID string
	sessions      map[string]setupSession
	wake          chan struct{}
	wg            sync.WaitGroup
}

func New(ctx context.Context, pool *sql.DB, db *gorm.DB, cfg config.Config, workers Workers, runtime ...RuntimeReloader) (*Manager, error) {
	path, err := filepath.Abs(cfg.Storage.Path)
	if err != nil {
		return nil, ErrDatabase
	}
	m := &Manager{root: ctx, pool: pool, db: db, cfg: cfg, workers: workers, store: newR2,
		staging:    filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".radar-backup-staging"),
		operations: make(map[string]Operation), sessions: make(map[string]setupSession), wake: make(chan struct{}, 1)}
	if len(runtime) > 0 {
		m.runtime = runtime[0]
	}
	if err := m.prepareStaging(); err != nil {
		return nil, err
	}
	state, err := m.readLive(ctx)
	if err != nil {
		return nil, err
	}
	m.state = state
	return m, nil
}

func (m *Manager) prepareStaging() error {
	if err := os.Mkdir(m.staging, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrSpace
	}
	info, err := os.Lstat(m.staging)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrSpace
	}
	if err := os.Chmod(m.staging, 0o700); err != nil {
		return ErrSpace
	}
	entries, err := os.ReadDir(m.staging)
	if err != nil {
		return ErrSpace
	}
	for _, entry := range entries {
		// 只清理此实例上次遗留的专用操作目录。
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "op-") {
			if err := os.RemoveAll(filepath.Join(m.staging, entry.Name())); err != nil {
				return ErrSpace
			}
		}
	}
	return nil
}

func (m *Manager) stagingDir() (string, error) {
	dir, err := os.MkdirTemp(m.staging, "op-")
	if err != nil {
		return "", ErrSpace
	}
	return dir, nil
}

func (m *Manager) readLive(ctx context.Context) (liveState, error) {
	var state liveState
	var err error
	state.initialized, err = storage.Initialized(ctx, m.db)
	if err != nil {
		return state, ErrDatabase
	}
	state.aiPaused, err = storage.BooleanSetting(ctx, m.db, storage.AutoAIResumeKey)
	if err != nil {
		return state, ErrDatabase
	}
	state.settings, err = readSettings(ctx, m.db)
	if err != nil {
		return state, err
	}
	runtime, err := (settings.Store{DB: m.db}).Get(ctx)
	if err != nil || runtime.Settings.Validate() != nil {
		return state, ErrDatabase
	}
	state.runtime = runtime.Settings
	if m.runtime == nil {
		state.restartRequired = !reflect.DeepEqual(runtime.Settings, m.cfg.RuntimeSettings())
	}
	rows := []storage.MaintenanceState{}
	if err := m.db.WithContext(ctx).Where(storage.MaintenanceStateColumns.Key+" IN ?", []string{receiptKey, lastBackupKey, backupDayKey, cleanupDayKey}).Find(&rows).Error; err != nil {
		return state, ErrDatabase
	}
	for _, row := range rows {
		switch row.Key {
		case receiptKey:
			if json.Unmarshal([]byte(row.Value), &state.receipt) != nil || state.receipt.OperationID == "" {
				return state, ErrDatabase
			}
		case lastBackupKey:
			if json.Unmarshal([]byte(row.Value), &state.lastBackup) != nil {
				return state, ErrDatabase
			}
		case backupDayKey:
			state.backupDay = row.Value
		case cleanupDayKey:
			state.cleanupDay = row.Value
		}
	}
	return state, nil
}

func readReceipt(ctx context.Context, db *gorm.DB) (receipt, error) {
	var row storage.MaintenanceState
	if err := db.WithContext(ctx).Find(&row, storage.MaintenanceStateColumns.Key+"=?", receiptKey).Error; err != nil {
		return receipt{}, ErrDatabase
	}
	var value receipt
	if row.Key != "" && (json.Unmarshal([]byte(row.Value), &value) != nil || value.OperationID == "") {
		return receipt{}, ErrDatabase
	}
	return value, nil
}

// Enter 在任何业务 SQL 前登记请求。排空时不再发出新的访问许可。
func (m *Manager) Enter(settingsWrite bool) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if settingsWrite && (m.busy == consts.OperationRestore || m.blocked) {
		return nil, ErrRestoring
	}
	if m.blocked {
		return nil, ErrMaintenance
	}
	if m.active == 0 {
		m.idle = make(chan struct{})
	}
	m.active++
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.active--
		if m.active == 0 {
			close(m.idle)
		}
	}, nil
}

func (m *Manager) Initialized() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.initialized
}

func (m *Manager) Settings() SettingsView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.view(time.Now())
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := m.state.view(time.Now())
	backup, _ := m.operationLocked(m.lastBackupID)
	cleanup, _ := m.operationLocked(m.lastCleanupID)
	restore, _ := m.operationLocked(m.lastRestoreID)
	if restore == nil && m.state.receipt.OperationID != "" {
		restore, _ = m.operationLocked(m.state.receipt.OperationID)
	}
	restart := m.state.restartRequired
	if m.runtime != nil {
		restart = m.runtime.RestartRequired()
	}
	return Status{Initialized: m.state.initialized, Restoring: m.busy == consts.OperationRestore, Maintenance: m.blocked,
		LastBackup: m.state.lastBackup, LastBackupOperation: backup, LastCleanup: cleanup, LastRestore: restore,
		NextBackupAt: view.NextBackupAt, NextCleanupAt: view.NextCleanupAt,
		AutoAIResumeRequired: m.state.aiPaused, RestartRequired: restart}
}

func (m *Manager) operationLocked(id string) (*Operation, error) {
	if op, ok := m.operations[id]; ok {
		return &op, nil
	}
	if id != "" && m.state.receipt.OperationID == id && !m.blocked && m.state.initialized {
		preparedAt := m.state.receipt.PreparedAt
		return &Operation{ID: id, Kind: consts.OperationRestore, Status: consts.StatusCompleted, Phase: consts.PhaseDone, DatabaseApplied: true,
			UpdatedAt: time.Now().UTC(), PreparedAt: &preparedAt}, nil
	}
	return nil, ErrOperation
}

func (m *Manager) Operation(id string) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.operationLocked(id)
}

func (m *Manager) update(id string, apply func(*Operation)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op := m.operations[id]
	apply(&op)
	op.UpdatedAt = time.Now().UTC()
	m.operations[id] = op
}

func (m *Manager) wakeup() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) checkIdleLocked() error {
	if m.busy == consts.OperationRestore || m.blocked {
		return ErrRestoring
	}
	if m.busy != "" {
		return ErrBusy
	}
	if m.root.Err() != nil {
		return ErrMaintenance
	}
	return nil
}

func (m *Manager) newOperationLocked(kind string) Operation {
	m.busy = kind
	op := Operation{ID: rand.Text(), Kind: kind, Status: consts.StatusRunning, Phase: consts.PhasePrepare, UpdatedAt: time.Now().UTC()}
	m.operations[op.ID] = op
	m.order = append(m.order, op.ID)
	if len(m.order) > 20 {
		delete(m.operations, m.order[0])
		m.order = m.order[1:]
	}
	if kind == consts.OperationBackup {
		m.lastBackupID = op.ID
	}
	if kind == consts.OperationCleanup {
		m.lastCleanupID = op.ID
	}
	if kind == consts.OperationRestore {
		m.lastRestoreID = op.ID
	}
	return op
}

func (m *Manager) launchLocked(op Operation, work func() error) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.finish(op.ID, work())
	}()
}

func (m *Manager) finish(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op := m.operations[id]
	if err != nil {
		op.Status, op.ErrorCode = consts.StatusFailed, ErrorCode(err)
	} else {
		op.Status, op.Phase, op.ErrorCode = consts.StatusCompleted, consts.PhaseDone, ""
	}
	op.UpdatedAt = time.Now().UTC()
	m.operations[id] = op
	m.busy = ""
	m.wakeup()
}

func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkIdleLocked(); err != nil {
		return err
	}
	if m.state.initialized {
		return ErrAlreadyInitialized
	}
	if err := storage.InitializeEmpty(ctx, m.db); err != nil {
		if errors.Is(err, storage.ErrAlreadyInitialized) {
			return ErrAlreadyInitialized
		}
		return ErrDatabase
	}
	m.state.initialized = true
	clear(m.sessions)
	if m.workers != nil {
		m.workers.Resume()
	}
	m.wakeup()
	return nil
}
