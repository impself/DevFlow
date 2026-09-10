// Package testutil 是集成测试的公共基建：真实 PG 连接、迁移、清场、夹具。
// 只被 _test.go 文件导入（包里没有运行时代码），T019 的协议测试继续复用。
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/impself/DevFlow/services/control/internal/store"
)

// Database 创建一个专属于本次测试二进制的临时数据库并返回连接串。
//
// 为什么不用共享库：`go test ./...` 会**并发**运行多个包的测试二进制，
// 共享一个库时 A 包的清场（DELETE）会删掉 B 包刚种的夹具——
// 这种竞态只在全套测试时偶发复现，单独跑永远绿，是集成测试最阴的坑。
// 每包一个库（进程 PID + 纳秒时间戳命名）从根上隔离；测试结束 DROP FORCE。
func Database(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("解析 TEST_DATABASE_URL: %v", err)
	}
	// 管理连接固定走 postgres 库（任何 PG 实例都有）
	adminURL := *u
	adminURL.Path = "/postgres"

	name := fmt.Sprintf("devflow_test_%d_%d", os.Getpid(), time.Now().UnixNano())

	admin, err := pgxpool.New(t.Context(), adminURL.String())
	if err != nil {
		t.Fatalf("连接管理库: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+name); err != nil {
		admin.Close()
		t.Fatalf("创建测试库 %s: %v", name, err)
	}
	t.Cleanup(func() {
		// Cleanup 阶段 t.Context() 已取消，清理 SQL 用 WithoutCancel；
		// WITH (FORCE) 踢掉残留连接再删（PG 13+）。
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(dropCtx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		admin.Close()
	})

	u.Path = "/" + name
	return u.String()
}

// NewPool 在专属临时库上建连接池（自动跳过无 TEST_DATABASE_URL 的单元环境）。
// 返回连接串供同一测试再开 Store 等——必须与池指向同一个库，否则夹具互不可见。
func NewPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dbURL := Database(t)
	pool, err := pgxpool.New(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dbURL
}

// Migrate 把 schema 迁到最新。
func Migrate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := store.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("迁移: %v", err)
	}
}

// Reset 清空业务表（保留迁移账本），保证每个测试都是全新现场。
func Reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{
		"model_calls", "actions", "approval_bundles", "reply_drafts", "artifacts",
		"run_commits", "runs", "inbox_events", "cases", "repositories", "operators",
	} {
		if _, err := pool.Exec(t.Context(), `DELETE FROM `+table); err != nil {
			t.Fatalf("清空 %s: %v", table, err)
		}
	}
}

// SeedRun 造一条可直接领取的 QUEUED run（仓库/case/事件/run 全套），
// 返回 run id。tag 用于日志可读区分。
func SeedRun(t *testing.T, pool *pgxpool.Pool, tag string) string {
	t.Helper()
	ctx := t.Context()
	suffix := fmt.Sprintf("%s-%d", tag, time.Now().UnixNano())

	if _, err := pool.Exec(ctx, `
		INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
		VALUES ($1, $2, 'o', $1, 'main', 7, 'v1')`, "repo-"+suffix, time.Now().UnixNano()); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbox_events (id, delivery_id, event_type, payload, signature_valid)
		VALUES ($1, $1, 'issues', '{}', true)`, "ev-"+suffix); err != nil {
		t.Fatalf("seed inbox_event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO cases (id, repo_id, issue_number, issue_id, title)
		VALUES ($1, $2, 1, $3, 't')`, "case-"+suffix, "repo-"+suffix, time.Now().UnixNano()); err != nil {
		t.Fatalf("seed case: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
		VALUES ($1, $2, $3, '{}')`, "run-"+suffix, "case-"+suffix, "ev-"+suffix); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return "run-" + suffix
}

// Eventually 轮询断言：cond 在 timeout 内变 true 则通过，否则 Fatal。
// 集成测试里的异步行为（worker 落库等）不能靠 sleep 猜，靠条件收敛。
func Eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("条件未在 %s 内满足: %s", timeout, msg)
}
