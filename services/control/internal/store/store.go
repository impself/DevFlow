package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// poolPingTimeout 限制启动探活的等待上限，与 T002 的健康检查超时同风格。
const poolPingTimeout = 5 * time.Second

// Store 是持久化的唯一门面：连接池 + 类型安全查询 + 事务助手。
// controller/publisher 只认 Store，不直接摸 pgxpool 或生成代码——
// 将来换驱动/加只读副本，改动被封在这一个包里。
type Store struct {
	pool *pgxpool.Pool
	*db.Queries
}

// Open 建立连接池并探活。惰性连接的陷阱（T002 教学笔记 §4）同理：
// 不 Ping 就不知道库通不通，问题会潜伏到第一个请求。
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("创建连接池: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, poolPingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库不可达: %w", err)
	}
	return &Store{pool: pool, Queries: db.New(pool)}, nil
}

func (s *Store) Close() { s.pool.Close() }

// WithTx 把「开启事务 → 执行 fn → 提交/回滚」收成一个函数作用域：
//   - fn 返回 nil：提交；
//   - fn 返回 error：回滚并把错误上抛；
//   - fn panic：defer 回滚兜底，事务绝不悬挂。
//
// 事务边界即业务边界（webhook 落库 + 建 case + 建 run 必须“要么都发生要么都没发生”），
// 用闭包表达边界让调用方不可能忘记提交或回滚。
func (s *Store) WithTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer tx.Rollback(ctx) // 提交后再回滚是 no-op，多这一次调用是保险不是 bug

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err // 错误保持原样上抛，回滚由 defer 完成
	}
	return tx.Commit(ctx)
}

// ErrNoRows 语义重导出：调用方判断「队列空了/查无此行」不必 import pgx。
var ErrNoRows = pgx.ErrNoRows

// IsUniqueViolation 判断错误是否为唯一约束冲突（可选指明约束名）。
// 这是错误语义映射的边界层：调用方问「是不是重复投递」，而不是拿着
// SQLSTATE 23505 字符串自己翻译——数据库方言被封在这里。
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
