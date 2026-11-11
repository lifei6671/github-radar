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
