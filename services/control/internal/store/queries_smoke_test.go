package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// setupQueries 迁移 + 清库后返回 Queries；给查询冒烟测试一个干净现场。
// T019 的并发协议测试将复用同一模式。
func setupQueries(t *testing.T) (*pgxpool.Pool, *db.Queries, context.Context) {
	t.Helper()
	pool := testPool(t)
	ctx := t.Context() // 测试结束时自动取消，泄露的查询会被及时掐断
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	for _, table := range []string{
		"model_calls", "actions", "approval_bundles", "reply_drafts", "artifacts",
		"run_commits", "runs", "inbox_events", "cases", "repositories", "operators",
	} {
		if _, err := pool.Exec(ctx, `DELETE FROM `+table); err != nil {
			t.Fatalf("清空 %s: %v", table, err)
		}
	}
	return pool, db.New(pool), ctx
}

// TestClaimSmoke 验证最小闭环：空库领取无行 → 种一条 QUEUED 能领到（epoch=1）→
// 领完即空。并发互斥/租约接管/提交冲突等完整协议在 T019 集成测试覆盖。
func TestClaimSmoke(t *testing.T) {
	pool, q, ctx := setupQueries(t)

	owner := "worker-1"
	// 断言 ErrNoRows 而不是 err == nil：连接故障也返回 error，不能让测试假绿
	if _, err := q.ClaimRun(ctx, &owner); !errors.Is(err, ErrNoRows) {
		t.Fatalf("空库领取应返回 ErrNoRows，得到 %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
		VALUES ('r-x', 42, 'o', 'n', 'main', 7, 'v1')`); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	if _, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
		ID: "ev-x", DeliveryID: "d-x", EventType: "issues",
		Payload: []byte("{}"), SignatureValid: true,
	}); err != nil {
		t.Fatalf("seed inbox_event: %v", err)
	}
	if _, err := q.UpsertCase(ctx, db.UpsertCaseParams{
		ID: "c-x", RepoID: "r-x", IssueNumber: 1, IssueID: 100, Title: "t",
	}); err != nil {
		t.Fatalf("seed case: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
		VALUES ('run-x', 'c-x', 'ev-x', '{}')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	claimed, err := q.ClaimRun(ctx, &owner)
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if claimed.ID != "run-x" || claimed.LeaseEpoch != 1 {
		t.Fatalf("领取结果异常: id=%s epoch=%d（期望 run-x/1）", claimed.ID, claimed.LeaseEpoch)
	}

	if _, err := q.ClaimRun(ctx, &owner); !errors.Is(err, ErrNoRows) {
		t.Fatalf("领取后再次领取应 ErrNoRows（QUEUED 未被消费），得到 %v", err)
	}
}
