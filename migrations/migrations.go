package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// SQL 迁移随二进制分发，启动与 migrate 子命令共用。
//
//go:embed *.sql
var files embed.FS

// Up 按版本应用未执行的迁移，返回本次应用的数量。
func Up(ctx context.Context, pool *sql.DB) (int, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, pool, files)
	if err != nil {
		return 0, fmt.Errorf("初始化 SQLite 迁移: %w", err)
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("执行 SQLite 迁移: %w", err)
	}
	return len(applied), nil
}

// BackupVersion 验证备份已执行的版本是当前内嵌迁移的连续前缀。
func BackupVersion(ctx context.Context, pool *sql.DB) (int64, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, pool, files)
	if err != nil {
		return 0, err
	}
	sources := provider.ListSources()
	rows, err := pool.QueryContext(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied=1 AND version_id>0 ORDER BY version_id")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var latest int64
	index := 0
	for rows.Next() {
		if err := rows.Scan(&latest); err != nil {
			return 0, err
		}
		if index >= len(sources) || sources[index].Version != latest {
			return 0, fmt.Errorf("备份包含不受支持的迁移版本 %d", latest)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if latest == 0 {
		return 0, fmt.Errorf("备份没有已执行的应用迁移")
	}
	return latest, nil
}
