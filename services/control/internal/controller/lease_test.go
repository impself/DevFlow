package controller_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// TestCommitRunResult 验证提交协议（AC23–26）的核心语义。
// 并发领取互斥等完整竞态场景在 T019 集成测试覆盖。
func TestCommitRunResult(t *testing.T) {
	commit := "commit-1"
	const hash = "sha256-abc"
	const outcome = "ANSWER_READY"

	setup := func(t *testing.T) (*store.Store, controller.Claim) {
		pool, dbURL := testutil.NewPool(t)
		testutil.Migrate(t, pool)
		testutil.Reset(t, pool)
		testutil.SeedRun(t, pool, "lease")

		st, err := store.Open(t.Context(), dbURL)
		if err != nil {
			t.Fatalf("打开 store: %v", err)
		}
		t.Cleanup(st.Close)

		owner := "worker-a"
		claim, err := st.ClaimRun(t.Context(), &owner)
		if err != nil {
			t.Fatalf("领取: %v", err)
		}
		return st, claim
	}

	t.Run("首次提交：回执+终态同事务落库", func(t *testing.T) {
		st, claim := setup(t)
		oc, err := controller.CommitRunResult(t.Context(), st, "worker-a", claim,
			commit, hash, []byte(`{"ok":true}`), outcome)
		if err != nil || oc != controller.CommitNew {
			t.Fatalf("应 CommitNew，得到 %v %v", oc, err)
		}
		// 直接 SQL 断言终态与回执
		pool := st.Pool()
		var status, outcomeCol string
		if err := pool.QueryRow(t.Context(),
			`SELECT status FROM runs WHERE id=$1`, claim.ID).Scan(&status); err != nil || status != "COMPLETED" {
			t.Fatalf("run 应 COMPLETED: status=%s err=%v", status, err)
		}
		if err := pool.QueryRow(t.Context(),
			`SELECT outcome FROM runs WHERE id=$1`, claim.ID).Scan(&outcomeCol); err != nil || outcomeCol != outcome {
			t.Fatalf("outcome 应 %s: %v", outcome, err)
		}
		if got := receiptCount(t, pool, claim.ID); got != 1 {
			t.Fatalf("应恰好 1 条回执，得到 %d", got)
		}
	})

	t.Run("重放同 id 同 hash：CommitDuplicated 不重复落回执（AC25）", func(t *testing.T) {
		st, claim := setup(t)
		if _, err := controller.CommitRunResult(t.Context(), st, "worker-a", claim, commit, hash, []byte(`{"ok":true}`), outcome); err != nil {
			t.Fatalf("首次提交: %v", err)
		}
		oc, err := controller.CommitRunResult(t.Context(), st, "worker-a", claim, commit, hash, []byte(`{"ok":true}`), outcome)
		if err != nil || oc != controller.CommitDuplicated {
			t.Fatalf("应 CommitDuplicated，得到 %v %v", oc, err)
		}
		if got := receiptCount(t, st.Pool(), claim.ID); got != 1 {
			t.Fatalf("重放不应新增回执，得到 %d", got)
		}
	})

	t.Run("同 id 不同 hash：ErrPayloadConflict（AC26）", func(t *testing.T) {
		st, claim := setup(t)
		if _, err := controller.CommitRunResult(t.Context(), st, "worker-a", claim, commit, hash, []byte(`{}`), outcome); err != nil {
			t.Fatalf("首次提交: %v", err)
		}
		_, err := controller.CommitRunResult(t.Context(), st, "worker-a", claim, commit, "sha256-different", []byte(`{}`), outcome)
		if !errors.Is(err, controller.ErrPayloadConflict) {
			t.Fatalf("应 ErrPayloadConflict，得到 %v", err)
		}
	})

	t.Run("旧纪元提交：ErrStaleEpoch 且不留回执（AC24）", func(t *testing.T) {
		st, claim := setup(t) // worker-a 已领取（epoch=1）
		pool := st.Pool()
		ctx := t.Context()

		// 模拟租约过期 → 清道夫翻 RECOVERING → worker-b 接管（epoch=2）
		if _, err := pool.Exec(ctx,
			`UPDATE runs SET lease_expires_at = now() - interval '1s' WHERE id=$1`, claim.ID); err != nil {
			t.Fatalf("制造过期: %v", err)
		}
		if rows, err := st.ExpireStaleRuns(ctx); err != nil || rows != 1 {
			t.Fatalf("清道夫应翻 1 行: rows=%d err=%v", rows, err)
		}
		ownerB := "worker-b"
		if _, err := st.ClaimRun(ctx, &ownerB); err != nil {
			t.Fatalf("worker-b 接管: %v", err)
		}

		// worker-a 拿着旧纪元来提交：必须被拒
		_, err := controller.CommitRunResult(ctx, st, "worker-a", claim, commit, hash, []byte(`{}`), outcome)
		if !errors.Is(err, controller.ErrStaleEpoch) {
			t.Fatalf("应 ErrStaleEpoch，得到 %v", err)
		}
		if got := receiptCount(t, pool, claim.ID); got != 0 {
			t.Fatalf("僵尸提交不应留下回执，得到 %d", got)
		}
		// run 仍归 worker-b，epoch=2
		var status string
		var epoch int64
		if err := pool.QueryRow(ctx, `SELECT status, lease_epoch FROM runs WHERE id=$1`, claim.ID).Scan(&status, &epoch); err != nil {
			t.Fatalf("查询 run: %v", err)
		}
		if status != "RUNNING" || epoch != 2 {
			t.Fatalf("run 应保持 RUNNING/epoch=2，得到 %s/%d", status, epoch)
		}
	})
}

func receiptCount(t *testing.T, pool *pgxpool.Pool, runID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM run_commits WHERE run_id=$1`, runID).Scan(&n); err != nil {
		t.Fatalf("统计回执: %v", err)
	}
	return n
}

// TestCancelRequested：取消检查的最小语义。
func TestCancelRequested(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	runID := testutil.SeedRun(t, pool, "cancel")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	requested, err := controller.CancelRequested(t.Context(), st, runID)
	if err != nil || requested {
		t.Fatalf("初始不应请求取消: %v %v", requested, err)
	}
	if _, err := st.Pool().Exec(t.Context(),
		`UPDATE runs SET status='CANCEL_REQUESTED' WHERE id=$1`, runID); err != nil {
		t.Fatalf("置 CANCEL_REQUESTED: %v", err)
	}
	if requested, err := controller.CancelRequested(t.Context(), st, runID); err != nil || !requested {
		t.Fatalf("应检出取消请求: %v %v", requested, err)
	}
}
