package backup

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

func openWAL(t *testing.T, path string) *sql.DB {
	t.Helper()
	cfg := config.Storage{Path: path, WAL: true, BusyTimeoutMS: 5000, MaxOpenConns: 4}
	pool, err := storage.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func sqlExec(t *testing.T, pool *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func fixtureDB(t *testing.T, path, value string) *sql.DB {
	t.Helper()
	pool := openWAL(t, path)
	sqlExec(t, pool, "CREATE TABLE restore_fixture (id INTEGER PRIMARY KEY, value TEXT, payload BLOB)")
	sqlExec(t, pool, "INSERT INTO restore_fixture VALUES (1, ?, zeroblob(3145728))", value)
	return pool
}

func requireValue(t *testing.T, row *sql.Row, want string) {
	t.Helper()
	var value string
	if err := row.Scan(&value); err != nil || value != want {
		t.Fatalf("value=%q err=%v want=%q", value, err, want)
	}
}

// 未 checkpoint 的 WAL、多连接与既有预编译语句均使用真实驱动验证。
func TestNativeRestoreSamePool(t *testing.T) {
	dir := t.TempDir()
	target := fixtureDB(t, filepath.Join(dir, "target.db"), "old")
	if wal := fileSize(filepath.Join(dir, "target.db-wal")); wal == 0 {
		t.Fatal("用例需要未 checkpoint 的 WAL")
	}
	sourcePath := filepath.Join(dir, "待 恢复#.db")
	fixtureDB(t, sourcePath, "new").Close()
	a, err := target.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := target.Conn(t.Context())
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	stmt, err := b.PrepareContext(t.Context(), "SELECT value FROM restore_fixture WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	requireValue(t, stmt.QueryRow(), "old")
	a.Close()
	b.Close()
	// database/sql 的池级语句会跨恢复保留。
	pooled, err := target.Prepare("SELECT value FROM restore_fixture WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pooled.Close()
	requireValue(t, pooled.QueryRow(), "old")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := nativeRestore(ctx, target, sourcePath, cancel); err == nil {
		t.Fatal("取消后恢复成功")
	}
	requireValue(t, pooled.QueryRow(), "old")
	if err := nativeRestore(t.Context(), target, sourcePath, nil); err != nil {
		t.Fatal(err)
	}
	requireValue(t, pooled.QueryRow(), "new")
	if err := checkSQLite(t.Context(), target); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRestoreCrashBoundaries(t *testing.T) {
	for _, mode := range []string{"midway", "committed"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target, source := filepath.Join(dir, "target.db"), filepath.Join(dir, "source.db")
			fixtureDB(t, target, "old").Close()
			fixtureDB(t, source, "new").Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeRestoreSubprocess$")
			cmd.Env = append(os.Environ(), "RADAR_RESTORE_TEST_MODE="+mode, "RADAR_RESTORE_TEST_TARGET="+target, "RADAR_RESTORE_TEST_SOURCE="+source)
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 91 {
				t.Fatalf("subprocess: %v %s", err, output)
			}
			pool := openWAL(t, target)
			want := "old"
			if mode == "committed" {
				want = "new"
			}
			requireValue(t, pool.QueryRow("SELECT value FROM restore_fixture"), want)
			if err := checkSQLite(t.Context(), pool); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeRestoreSubprocess(t *testing.T) {
	mode := os.Getenv("RADAR_RESTORE_TEST_MODE")
	if mode == "" {
		return
	}
	pool := openWAL(t, os.Getenv("RADAR_RESTORE_TEST_TARGET"))
	var between func()
	if mode == "midway" {
		between = func() { os.Exit(91) }
	}
	if err := nativeRestore(t.Context(), pool, os.Getenv("RADAR_RESTORE_TEST_SOURCE"), between); err != nil {
		t.Fatal(err)
	}
	if mode == "committed" {
		os.Exit(91)
	}
	t.Fatal("未触发中途退出")
}
