package store

import (
	"context"
	"errors"
	"testing"

	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// TestWithTx 验证事务助手的两种命运：fn 成功→提交落库；fn 失败→回滚无痕。
func TestWithTx(t *testing.T) {
	pool, _, ctx := setupQueries(t)
	s := &Store{pool: pool, Queries: db.New(pool)}

	// seedRepo 幂等 + 子测试级清理 case 行：夹具生命周期跟着注册它的 subtest 走，
	// 避免「上一个子测试的残留让下一个子测试 seed 撞主键」。
	seedRepo := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			if _, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM cases WHERE id LIKE 'c-tx-%'`); err != nil {
				t.Errorf("清理 case 夹具: %v", err)
			}
		})
		if _, err := pool.Exec(ctx, `
			INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
			VALUES ('r-tx', 43, 'o', 'n', 'main', 7, 'v1')
			ON CONFLICT (id) DO NOTHING`); err != nil {
			t.Fatalf("seed repository: %v", err)
		}
	}
	t.Cleanup(func() {
		// WithoutCancel：父测试级 cleanup 执行时 ctx 已被 t.Context() 取消，
		// 直接用会静默失败留下残留行（review P1-1 实证过）。
		if _, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM repositories WHERE id='r-tx'`); err != nil {
			t.Errorf("清理 repository 夹具: %v", err)
		}
	})

	t.Run("fn 成功则提交", func(t *testing.T) {
		seedRepo(t)
		err := s.WithTx(ctx, func(q *db.Queries) error {
			_, err := q.UpsertCase(ctx, db.UpsertCaseParams{
				ID: "c-tx-1", RepoID: "r-tx", IssueNumber: 1, IssueID: 200, Title: "t",
			})
			return err
		})
		if err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cases WHERE id='c-tx-1'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("提交后应能查到 case: n=%d err=%v", n, err)
		}
	})

	t.Run("fn 失败则回滚", func(t *testing.T) {
		seedRepo(t)
		boom := errors.New("boom")
		err := s.WithTx(ctx, func(q *db.Queries) error {
			if _, err := q.UpsertCase(ctx, db.UpsertCaseParams{
				ID: "c-tx-2", RepoID: "r-tx", IssueNumber: 2, IssueID: 201, Title: "t",
			}); err != nil {
				return err
			}
			return boom // 故意在第二个写之后失败
		})
		if !errors.Is(err, boom) {
			t.Fatalf("应原样上抛 boom，得到 %v", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cases WHERE id='c-tx-2'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("回滚后不应有 c-tx-2: n=%d err=%v", n, err)
		}
	})

	t.Run("唯一冲突可被语义判断", func(t *testing.T) {
		seedRepo(t)
		err := s.WithTx(ctx, func(q *db.Queries) error {
			if _, err := q.UpsertCase(ctx, db.UpsertCaseParams{
				ID: "c-tx-3", RepoID: "r-tx", IssueNumber: 3, IssueID: 202, Title: "t",
			}); err != nil {
				return err
			}
			// issue_id 全局唯一，再次插入同 issue_id 必撞唯一约束
			_, err := q.UpsertCase(ctx, db.UpsertCaseParams{
				ID: "c-tx-4", RepoID: "r-tx", IssueNumber: 4, IssueID: 202, Title: "t",
			})
			return err
		})
		if err == nil {
			t.Fatal("同 issue_id 插入竟成功")
		}
		if !IsUniqueViolation(err, "cases_issue_id_key") {
			t.Fatalf("应识别为唯一约束冲突(cases_issue_id_key): %v", err)
		}
		if IsUniqueViolation(err, "other_constraint") {
			t.Fatal("约束名不匹配时不应误判")
		}
	})
}
