package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool 连接集成测试库；未设置 TEST_DATABASE_URL 时跳过（单元环境无 PG）。
// 本地起库：cd infra && docker compose up -d，然后
// TEST_DATABASE_URL=postgres://devflow:devflow@localhost:5432/devflow?sslmode=disable go test ./...
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestMigrateIdempotent 是迁移的核心验收：连跑两遍不炸、表齐全。
func TestMigrateIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("第一遍迁移: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("第二遍迁移（应全部跳过）: %v", err)
	}

	wantTables := []string{
		"operators", "repositories", "inbox_events", "cases", "runs",
		"run_commits", "artifacts", "reply_drafts", "approval_bundles",
		"actions", "model_calls",
	}
	for _, table := range wantTables {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			   WHERE table_schema='public' AND table_name=$1)`, table,
		).Scan(&exists)
		if err != nil || !exists {
			t.Errorf("表 %s 不存在: err=%v exists=%v", table, err, exists)
		}
	}
}

// TestSchemaConstraints 冒烟验证三条协议级约束（详见 data-model.md）。
// 用 setupQueries 清场：夹具不与历史残留冲突，测试顺序无关。
func TestSchemaConstraints(t *testing.T) {
	pool, _, ctx := setupQueries(t)

	t.Run("delivery_id 唯一（AC45 投递去重）", func(t *testing.T) {
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM inbox_events WHERE id IN ('ev1','ev2')`)
		})
		ins := `INSERT INTO inbox_events (id, delivery_id, event_type, payload, signature_valid)
		        VALUES ($1,$2,'issues','{}',true)`
		if _, err := pool.Exec(ctx, ins, "ev1", "d1"); err != nil {
			t.Fatalf("首次插入失败: %v", err)
		}
		if _, err := pool.Exec(ctx, ins, "ev2", "d1"); err == nil {
			t.Fatal("重复 delivery_id 竟然插入成功，唯一约束未生效")
		}
	})

	t.Run("model_calls 费用不得记零（AC47）", func(t *testing.T) {
		// 借一条合法 run 作外键锚（用例结束后按依赖逆序清理，不污染协议测试）
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM model_calls WHERE id IN ('mc-zero','mc-ok')`)
			pool.Exec(ctx, `DELETE FROM runs WHERE id='run1'`)
			pool.Exec(ctx, `DELETE FROM inbox_events WHERE id='ev-r1'`)
			pool.Exec(ctx, `DELETE FROM cases WHERE id='c1'`)
			pool.Exec(ctx, `DELETE FROM repositories WHERE id='r1'`)
		})
		if _, err := pool.Exec(ctx, `
			INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
			VALUES ('r1', 1, 'o', 'n', 'main', 1, 'v1') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 repository: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO cases (id, repo_id, issue_number, issue_id, title)
			VALUES ('c1', 'r1', 1, 1001, 't') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 case: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO inbox_events (id, delivery_id, event_type, payload, signature_valid)
			VALUES ('ev-r1', 'd-r1', 'issues', '{}', true) ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 event: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
			VALUES ('run1', 'c1', 'ev-r1', '{}') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 run: %v", err)
		}

		ins := `INSERT INTO model_calls (id, run_id, model, cost_cny, cost_status)
		        VALUES ($1, 'run1', 'qwen-plus', $2, $3)`
		if _, err := pool.Exec(ctx, ins, "mc-zero", 0, "estimated"); err == nil {
			t.Fatal("cost_cny=0 竟然插入成功，CHECK 约束未生效")
		}
		if _, err := pool.Exec(ctx, ins, "mc-ok", 0.01, "estimated"); err != nil {
			t.Fatalf("合法费用插入失败: %v", err)
		}
	})

	t.Run("runs 状态受 CHECK 约束", func(t *testing.T) {
		// 上一个子测试的 cleanup 已删除 run1，这里自带夹具（cleanup 在子测试结束时执行）
		if _, err := pool.Exec(ctx, `
			INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
			VALUES ('r2', 2, 'o', 'n2', 'main', 2, 'v1') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 repository: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO cases (id, repo_id, issue_number, issue_id, title)
			VALUES ('c2', 'r2', 2, 1002, 't') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 case: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO inbox_events (id, delivery_id, event_type, payload, signature_valid)
			VALUES ('ev-r2', 'd-r2', 'issues', '{}', true) ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 event: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
			VALUES ('run2', 'c2', 'ev-r2', '{}') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("插入 run: %v", err)
		}
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM runs WHERE id='run2'`)
			pool.Exec(ctx, `DELETE FROM inbox_events WHERE id='ev-r2'`)
			pool.Exec(ctx, `DELETE FROM cases WHERE id='c2'`)
			pool.Exec(ctx, `DELETE FROM repositories WHERE id='r2'`)
		})

		_, err := pool.Exec(ctx,
			`UPDATE runs SET status='DREAMING' WHERE id='run2'`)
		if err == nil {
			t.Fatal("非法状态竟更新成功，CHECK 约束未生效")
		}
	})
}
