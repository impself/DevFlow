// Package store 收口所有持久化：迁移、sqlc 生成的查询、连接池与事务辅助。
// 业务数据只有 Go 侧写（宪法 IV）；Python 与前端一律经 API 读。
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate 把 migrations/ 下未应用的 .sql 按文件名序应用到位。
//
// 幂等机制：schema_migrations 是"账本"，记录已应用的版本号；重跑时逐条比对，
// 已应用即跳过——Checkpoint 要求「迁移可重复执行」由此保证。
// 每个迁移在独立事务中执行：单个迁移要么整体生效要么整体回滚，
// 不允许出现"建了一半的表"。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("创建 schema_migrations 账本: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("读取迁移目录: %w", err)
	}
	sort.Strings(entries) // 文件名即版本序：0001_ < 0002_ < …

	for _, name := range entries {
		version := name
		var already bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version,
		).Scan(&already)
		if err != nil {
			return fmt.Errorf("查询迁移账本: %w", err)
		}
		if already {
			continue
		}

		sqlBytes, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return fmt.Errorf("读取迁移 %s: %w", name, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("开启迁移事务 %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("执行迁移 %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("登记迁移 %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("提交迁移 %s: %w", name, err)
		}
	}
	return nil
}
