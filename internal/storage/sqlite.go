package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	gormsqlite "github.com/libtnb/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/lifei6671/github-radar/internal/config"
)

// IsBusy 识别 SQLite 的临时锁竞争，包括扩展 BUSY 错误码。
func IsBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

// Open 创建数据库目录和连接池，并确认 SQLite 实际采用的日志模式。
func Open(ctx context.Context, cfg config.Storage) (*sql.DB, error) {
	return open(ctx, cfg, true)
}

// OpenBootstrap 仅用于迁移和读取启动设置，不切换已有数据库的日志模式。
func OpenBootstrap(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, config.Storage{Path: path, BusyTimeoutMS: 5000, MaxOpenConns: 1}, false)
}

func open(ctx context.Context, cfg config.Storage, configureJournal bool) (*sql.DB, error) {
	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("解析 SQLite 路径: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建 SQLite 数据目录: %w", err)
	}

	// 使用文件 URI 保留路径中的空格、中文和 URI 保留字符。
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	// 外键和忙等待是连接级设置，驱动在每次打开连接时应用。
	params := url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", cfg.BusyTimeoutMS), "foreign_keys(ON)"},
		// 任务和准入事务先取得写锁，避免读到旧快照后升级写锁失败。
		"_txlock": {"immediate"},
	}
	dsn := url.URL{Scheme: "file", Path: uriPath, RawQuery: params.Encode()}
	connector, err := sqlite.NewConnector(dsn.String())
	if err != nil {
		return nil, fmt.Errorf("配置 SQLite 连接: %w", err)
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	pool.SetMaxIdleConns(cfg.MaxOpenConns)

	var openErr error
	if configureJournal {
		openErr = setJournalMode(ctx, pool, cfg.WAL)
	} else {
		openErr = pool.PingContext(ctx)
	}
	if openErr != nil {
		pool.Close()
		return nil, openErr
	}
	// 业务库会保存 R2 凭据，原文件和后续 SQLite 辅助文件需沿用受限权限。
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && (suffix == "" || !errors.Is(err, os.ErrNotExist)) {
			pool.Close()
			return nil, fmt.Errorf("设置 SQLite 文件权限: %w", err)
		}
	}
	return pool, nil
}

func setJournalMode(ctx context.Context, pool *sql.DB, wal bool) error {
	mode := "delete"
	if wal {
		mode = "wal"
	}
	var actual string
	if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode = "+mode).Scan(&actual); err != nil {
		return fmt.Errorf("设置 SQLite journal_mode: %w", err)
	}
	if !strings.EqualFold(actual, mode) {
		return fmt.Errorf("SQLite journal_mode 实际为 %s，配置要求 %s", actual, mode)
	}
	return nil
}

// OpenGORM 使用已完成初始化的同一个连接池执行模型读写。
func OpenGORM(pool *sql.DB) (*gorm.DB, error) {
	return gorm.Open(gormsqlite.New(gormsqlite.Config{Conn: pool}), &gorm.Config{
		Logger: logger.Discard,
	})
}
