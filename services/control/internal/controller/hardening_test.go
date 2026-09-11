package controller_test

// 调研驱动加固项的协议测试：重试预算（毒 run 终结）与心跳 status 守卫。

import (
	"errors"
	"testing"

	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// TestRetryBudget：耗尽预算的 run 不可领取，清道夫判 FAILED（调研 #6）。
func TestRetryBudget(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	runID := testutil.SeedRun(t, pool, "poison")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()

	// 领取后人为耗尽预算（模拟反复崩溃）：attempts 已是 1，把 max 压到 1
	owner := "worker-poison"
	if _, err := st.ClaimRun(ctx, &owner); err != nil {
		t.Fatalf("领取: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET max_attempts = 1 WHERE id=$1`, runID); err != nil {
		t.Fatalf("压预算: %v", err)
	}
	// 模拟崩溃：租约过期 → 翻 RECOVERING
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET lease_expires_at = now() - interval '1s' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.ExpireStaleRuns(ctx); err != nil || rows != 1 {
		t.Fatalf("应翻 RECOVERING: rows=%d err=%v", rows, err)
	}

	// 预算耗尽 → 不可再领取（领取 SQL 的 attempts < max_attempts 过滤）
	if _, err := st.ClaimRun(ctx, &owner); !errors.Is(err, store.ErrNoRows) {
		t.Fatalf("耗尽预算的 run 不可领取: %v", err)
	}

	// 清道夫第二步：终结为 FAILED
	if rows, err := st.FailExhaustedRuns(ctx); err != nil || rows != 1 {
		t.Fatalf("应终结 1 条: rows=%d err=%v", rows, err)
	}
	var status, runError string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(error,'') FROM runs WHERE id=$1`, runID).Scan(&status, &runError); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || runError == "" {
		t.Fatalf("应 FAILED 带原因: %s %q", status, runError)
	}
}

// TestHeartbeatStatusGuard：reaper 翻 RECOVERING 后，迟到心跳不得复活任务（调研 #3）。
func TestHeartbeatStatusGuard(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	runID := testutil.SeedRun(t, pool, "guard")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()

	owner := "worker-guard"
	claim, err := st.ClaimRun(ctx, &owner)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}

	// 正常心跳命中（RUNNING 且身份匹配）
	hb := db.HeartbeatRunParams{ID: claim.ID, LeaseOwner: &owner, LeaseEpoch: claim.LeaseEpoch}
	if rows, err := st.HeartbeatRun(ctx, hb); err != nil || rows != 1 {
		t.Fatalf("正常心跳应命中: rows=%d err=%v", rows, err)
	}

	// 清道夫翻 RECOVERING（epoch/owner 未变——尚未被接管）
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET lease_expires_at = now() - interval '1s' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.ExpireStaleRuns(ctx); err != nil || rows != 1 {
		t.Fatalf("应翻 RECOVERING: rows=%d err=%v", rows, err)
	}

	// 迟到心跳：owner/epoch 都还匹配，但 status 已非 RUNNING → 必须 0 行
	if rows, err := st.HeartbeatRun(ctx, hb); err != nil || rows != 0 {
		t.Fatalf("迟到心跳不得复活: rows=%d err=%v", rows, err)
	}
}
