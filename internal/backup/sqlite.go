package backup

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

// checkSQLite 同时检查文件结构和外键。检查在独立的临时库连接上执行。
func checkSQLite(ctx context.Context, pool *sql.DB) error {
	var result string
	if err := pool.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		return Fault(consts.CodeBackupIntegrityFailed)
	}
	rows, err := pool.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return Fault(consts.CodeBackupIntegrityFailed)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		return Fault(consts.CodeBackupForeignKeyFailed)
	}
	return nil
}

func snapshot(ctx context.Context, pool *sql.DB, path string) (int64, error) {
	if _, err := pool.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return 0, ErrDatabase
	}
	copyDB, err := storage.OpenBootstrap(ctx, path)
	if err != nil {
		return 0, ErrDatabase
	}
	defer copyDB.Close()
	if err := checkSQLite(ctx, copyDB); err != nil {
		return 0, err
	}
	return migrations.BackupVersion(ctx, copyDB)
}

func pageSize(ctx context.Context, db *sql.DB) (int64, error) {
	var size int64
	err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size)
	if err != nil || size <= 0 {
		return 0, ErrDatabase
	}
	return size, nil
}

// nativeRestore 保留原 sql.DB。只有 Step 到达 DONE 且 Finish 成功才返回成功。
func nativeRestore(ctx context.Context, target *sql.DB, sourcePath string, between func()) error {
	conn, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	path := filepath.ToSlash(sourcePath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	source := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	return conn.Raw(func(raw any) (final error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		driver, ok := raw.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite 驱动缺少原生恢复接口")
		}
		copy, err := driver.NewRestore(source.String())
		if err != nil {
			return err
		}
		defer func() { final = errors.Join(final, copy.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := copy.Step(128)
			if err != nil || !more {
				return err
			}
			if between != nil {
				between()
			}
		}
	})
}
