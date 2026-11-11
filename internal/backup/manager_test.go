package backup

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

type s3FixtureObject struct {
	body    []byte
	headers http.Header
}

// s3Fixture 走生产 SDK 的签名请求、分页、元数据、读写和删除。
type s3Fixture struct {
	mu           sync.Mutex
	objects      map[string]s3FixtureObject
	denyPut      bool
	loseResponse bool
}

func (s *s3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Authorization"), "Credential=fixture-") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/radar-test")
	key = strings.TrimPrefix(key, "/")
	if r.URL.Query().Get("list-type") == "2" {
		type entry struct{ Key string }
		result := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []entry
			IsTruncated           bool
			NextContinuationToken string `xml:",omitempty"`
		}{}
		// 模拟 R2 的多页结果，并故意返回越界键以校验清理边界。
		if r.URL.Query().Get("continuation-token") == "" {
			result.IsTruncated, result.NextContinuationToken = true, "next"
		} else {
			for k := range s.objects {
				result.Contents = append(result.Contents, entry{k})
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(result)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if s.denyPut {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
			return
		}
		body, err := io.ReadAll(r.Body)
		md := md5.Sum(body)
		if err != nil || r.Header.Get("Content-Md5") != base64.StdEncoding.EncodeToString(md[:]) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h := make(http.Header)
		for k, v := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
				h[k] = v
			}
		}
		s.objects[key] = s3FixtureObject{body, h}
		if s.loseResponse {
			s.loseResponse = false
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
	case http.MethodHead, http.MethodGet:
		object, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for k, v := range object.headers {
			w.Header()[k] = v
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
			return
		}
		_, _ = w.Write(object.body)
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func testManager(t *testing.T, path string, initialize bool, workers Workers) (*Manager, *gorm.DB, config.Config) {
	t.Helper()
	ctx := t.Context()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = path
	pool := openWAL(t, path)
	if _, err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := (settings.Store{DB: db}).Initialize(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithRuntime(runtime.Settings)
	m, err := New(ctx, pool, db, cfg, workers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Wait)
	if initialize {
		if err := m.Initialize(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return m, db, cfg
}

func waitOperation(t *testing.T, m *Manager, id string, want string) Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		value, err := m.Operation(id)
		if err != nil {
			t.Fatal(err)
		}
		if value.Status != "running" {
			if value.Status != want {
				t.Fatalf("operation %+v want %s", value, want)
			}
			return *value
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("操作超过验证时限")
	return Operation{}
}

func connection(id string) Connection {
	return Connection{Endpoint: "https://" + strings.Repeat("a", 32) + ".r2.cloudflarestorage.com", Bucket: "radar-test", Prefix: "radar/backups/", AccessKeyID: "fixture-" + id, SecretAccessKey: "fixture-secret-" + id}
}

type drainFixture struct {
	started chan struct{}
}

func (d drainFixture) Pause(ctx context.Context) error { close(d.started); return nil }
func (d drainFixture) Resume()                         {}

func TestR2BackupRetentionAndSetupRestore(t *testing.T) {
	ctx := t.Context()
	server := &s3Fixture{objects: make(map[string]s3FixtureObject)}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	factory := func(c Connection) objectStore { c.Endpoint = httpServer.URL; return newR2(c) }
	dir := t.TempDir()
	source, db, _ := testManager(t, filepath.Join(dir, "source.db"), true, nil)
	source.store = factory
	cfg := defaults()
	cfg.R2 = connection("old")
	if _, err := source.Save(ctx, SettingsUpdate{Enabled: false, Schedule: cfg.Schedule, RetentionDays: 5, R2: &cfg.R2}); err != nil {
		t.Fatal(err)
	}
	if err := setMark(db, "fixture.saved", "from backup"); err != nil {
		t.Fatal(err)
	}
	job := storage.Job{Type: "discover_search", DedupKey: "fixture:old", PayloadJSON: "{}", Status: "pending", MaxAttempts: 3}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	call := storage.AICall{CallKey: "fixture-call", JobID: job.ID, Provider: "fixture", Origin: "auto", Status: "sending", ReservedAt: time.Now().UnixMilli()}
	if err := db.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storage.RateLimit{Resource: "core", LimitTotal: 5000, Remaining: 100}).Error; err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	for _, key := range []string{"radar/backups/2024-12-27.db.gz", "radar/backups/2024-12-28.db.gz", "radar/backups/2024-12-31.db.gz",
		"radar/backups/2024-02-30.db.gz", "radar/backups/archive/2024-12-25.db.gz", "other/2024-12-25.db.gz"} {
		server.objects[key] = s3FixtureObject{}
	}
	server.mu.Unlock()
	// UTC 的 12 月 31 日在计划时区已是 1 月 1 日。
	date := time.Date(2024, 12, 31, 20, 0, 0, 0, time.UTC)
	if err := cleanObjects(ctx, factory(cfg.R2), cfg, date); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	if _, ok := server.objects["radar/backups/2024-12-27.db.gz"]; ok || len(server.objects) != 5 {
		server.mu.Unlock()
		t.Fatalf("unexpected retention: %v", server.objects)
	}
	server.loseResponse = true
	server.mu.Unlock()
	op, err := source.StartBackup()
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, source, op.ID, "completed")
	last := source.Status().LastBackup
	if last == nil || last.RawSize == 0 || last.SHA256 == "" {
		t.Fatalf("last backup %+v", last)
	}
	server.mu.Lock()
	server.denyPut = true
	server.mu.Unlock()
	failed, err := source.StartBackup()
	if err != nil {
		t.Fatal(err)
	}
	if failed := waitOperation(t, source, failed.ID, "failed"); failed.ErrorCode != string(ErrR2) {
		t.Fatalf("明确拒绝被误判: %+v", failed)
	}
	server.mu.Lock()
	server.denyPut = false
	server.mu.Unlock()
	page, err := source.List(ctx, "", false, 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].SHA256 != last.SHA256 {
		t.Fatalf("failed PUT changed today's object: %+v %v", page, err)
	}

	workers := drainFixture{started: make(chan struct{})}
	target, targetDB, targetCfg := testManager(t, filepath.Join(dir, "target.db"), false, workers)
	target.store = factory
	targetConn := connection("new")
	id, _, err := target.CreateSession(ctx, targetConn)
	if err != nil {
		t.Fatal(err)
	}
	release, err := target.Enter(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	restore, err := target.StartRestore(last.ObjectKey, nil, id, true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-workers.started:
	case <-time.After(10 * time.Second):
		t.Fatal("没有进入维护模式")
	}
	if _, err := target.Enter(false); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("业务门禁: %v", err)
	}
	if _, err := target.Enter(true); !errors.Is(err, ErrRestoring) {
		t.Fatalf("写配置门禁: %v", err)
	}
	if state := target.Status(); !state.Maintenance || state.LastRestore.DatabaseApplied {
		t.Fatalf("排空状态: %+v", state)
	}
	release()
	release = nil
	restored := waitOperation(t, target, restore.ID, "completed")
	if !restored.DatabaseApplied || !target.Initialized() || !target.Status().AutoAIResumeRequired {
		t.Fatalf("restore: %+v", restored)
	}
	requireValue(t, target.pool.QueryRow("SELECT value FROM maintenance_state WHERE key='fixture.saved'"), "from backup")
	requireValue(t, target.pool.QueryRow("SELECT last_error_code FROM jobs WHERE id=?", job.ID), "restore_reset")
	requireValue(t, target.pool.QueryRow("SELECT error_code FROM ai_calls WHERE call_key=?", call.CallKey), "restore_reset_unknown")
	requireValue(t, target.pool.QueryRow("SELECT CAST(COUNT(*) AS TEXT) FROM rate_limits"), "0")
	saved, err := readSettings(ctx, targetDB)
	if err != nil || saved.R2 != targetConn || strings.Contains(target.Settings().R2.AccessKeyIDMasked, targetConn.AccessKeyID) {
		t.Fatalf("连接未原子替换: %v", err)
	}
	if err := target.Initialize(ctx); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("安装入口: %v", err)
	}
	restarted, err := New(ctx, target.pool, targetDB, targetCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := restarted.Operation(restore.ID)
	if err != nil || receipt.Status != "completed" || receipt.PreparedAt == nil || !restarted.Status().AutoAIResumeRequired {
		t.Fatalf("持久化恢复凭证: %+v %v", receipt, err)
	}
	if err := restarted.ResumeAI(ctx); err != nil {
		t.Fatal(err)
	}
	if paused, err := storage.BooleanSetting(ctx, targetDB, storage.AutoAIResumeKey); paused || err != nil {
		t.Fatalf("自动 AI 标记: %v %v", paused, err)
	}

	// 制造已提交后的加载超时，验证重载继续使用原操作与原库。
	restarted.mu.Lock()
	restarted.blocked, restarted.busy = true, "restore"
	receipt.Status, receipt.Phase = "running", "reload"
	restarted.operations[restore.ID] = *receipt
	restarted.mu.Unlock()
	expired, cancel := context.WithCancel(ctx)
	cancel()
	restarted.finish(restore.ID, restarted.reload(expired, restore.ID))
	result, err := restarted.Operation(restore.ID)
	if err != nil || !result.DatabaseApplied || result.ErrorCode != "restore_reload_failed" || !restarted.Status().Maintenance {
		t.Fatalf("提交后的加载状态: %+v %v", result, err)
	}
	reload, err := restarted.Reload()
	if err != nil || reload.ID != restore.ID {
		t.Fatalf("重载原操作: %+v %v", reload, err)
	}
	waitOperation(t, restarted, reload.ID, "completed")
	if restarted.Status().Maintenance {
		t.Fatal("重载后仍处于维护")
	}

	// 远端内容变化但 HEAD 仍返回原元数据，恢复必须在停服前失败。
	server.mu.Lock()
	corrupted := server.objects[last.ObjectKey]
	corrupted.body[0] ^= 0xff
	server.objects[last.ObjectKey] = corrupted
	server.mu.Unlock()
	restarted.store = factory
	bad, err := restarted.StartRestore(last.ObjectKey, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result := waitOperation(t, restarted, bad.ID, "failed"); result.ErrorCode != "backup_checksum_mismatch" || result.DatabaseApplied || restarted.Status().Maintenance {
		t.Fatalf("错误摘要的恢复结果: %+v", result)
	}
	requireValue(t, restarted.pool.QueryRow("SELECT value FROM maintenance_state WHERE key='fixture.saved'"), "from backup")
}

func TestAutomaticScheduleRecordsDailyAttempts(t *testing.T) {
	m, db, _ := testManager(t, filepath.Join(t.TempDir(), "radar.db"), true, nil)
	s3 := &s3Fixture{objects: make(map[string]s3FixtureObject)}
	server := httptest.NewServer(s3)
	defer server.Close()
	m.store = func(c Connection) objectStore { c.Endpoint = server.URL; return newR2(c) }
	cfg := defaults()
	cfg.R2 = connection("scheduled")
	if _, err := m.Save(t.Context(), SettingsUpdate{Enabled: true, Schedule: cfg.Schedule, RetentionDays: 5, R2: &cfg.R2}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 19, 31, 0, 0, time.UTC)
	if due := m.state.view(at).NextBackupAt; due == nil || !due.Equal(at) {
		t.Fatalf("补跑时间: %v", due)
	}
	m.schedule(t.Context(), at)
	first := m.Status().LastBackupOperation
	if first == nil {
		t.Fatal("没有启动到期备份")
	}
	waitOperation(t, m, first.ID, "completed")
	before := at.Add(-time.Hour)
	tomorrow := atDate(before.In(cfg.location()).AddDate(0, 0, 1), cfg.Schedule.Hour, cfg.Schedule.Minute)
	manual := m.state
	manual.backupDay = ""
	if next := manual.view(before).NextBackupAt; next == nil || !next.Equal(tomorrow) {
		t.Fatalf("当天已备份，下次应为次日: %v", next)
	}
	m.schedule(t.Context(), at)
	cleanup := m.Status().LastCleanup
	if cleanup == nil {
		t.Fatal("没有启动独立保留清理")
	}
	waitOperation(t, m, cleanup.ID, "completed")
	m.schedule(t.Context(), at.Add(time.Minute))
	state := m.Status()
	if state.LastBackupOperation.ID != first.ID || state.LastCleanup.ID != cleanup.ID {
		t.Fatal("重复调度同一天")
	}
	page, err := m.List(t.Context(), "", false, 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Date != "2026-01-02" {
		t.Fatalf("上传后清理误删本次快照: %+v %v", page, err)
	}
	requireValue(t, m.pool.QueryRow("SELECT value FROM maintenance_state WHERE key=?", backupDayKey), "Asia/Shanghai/2026-01-02/03:00")
	settings, err := readSettings(t.Context(), db)
	if err != nil || !settings.Enabled {
		t.Fatalf("计划未持久化: %v", err)
	}
}

func TestRestoreRejectsWALPageSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large-page.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	sqlExec(t, raw, "PRAGMA page_size=8192")
	sqlExec(t, raw, "VACUUM")
	raw.Close()
	source, _, _ := testManager(t, path, true, nil)
	s3 := &s3Fixture{objects: make(map[string]s3FixtureObject)}
	server := httptest.NewServer(s3)
	defer server.Close()
	factory := func(c Connection) objectStore { c.Endpoint = server.URL; return newR2(c) }
	source.store = factory
	cfg := defaults()
	cfg.R2 = connection("page")
	if _, err := source.Save(t.Context(), SettingsUpdate{Schedule: cfg.Schedule, RetentionDays: 5, R2: &cfg.R2}); err != nil {
		t.Fatal(err)
	}
	op, err := source.StartBackup()
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, source, op.ID, "completed")
	target, _, _ := testManager(t, filepath.Join(dir, "target.db"), false, nil)
	target.store = factory
	id, _, err := target.CreateSession(t.Context(), cfg.R2)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := target.StartRestore(source.Status().LastBackup.ObjectKey, nil, id, true)
	if err != nil {
		t.Fatal(err)
	}
	result := waitOperation(t, target, rest.ID, "failed")
	if result.ErrorCode != "restore_page_size_mismatch" || target.Initialized() || target.Status().Maintenance {
		t.Fatalf("恢复页大小错误: %+v", result)
	}
}
