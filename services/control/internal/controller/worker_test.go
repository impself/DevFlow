package controller_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// testStore 在 pool 所属的同一个临时库上打开 Store——
// 两个连接必须指向同一个库，否则夹具互不可见。
func testStore(t *testing.T, dbURL string) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// TestWorkerHappyPath：种一条 QUEUED → worker 领取 → 钩子执行 → 终态 COMPLETED。
// 这是 Runner 骨架的最小闭环验收；租约接管/失权协议在 T019 集成测试覆盖。
func TestWorkerHappyPath(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	runID := testutil.SeedRun(t, pool, "happy")

	st := testStore(t, dbURL)

	var calls atomic.Int32
	execute := func(ctx context.Context, claim controller.Claim) (string, error) {
		calls.Add(1)
		return "ANSWER_READY", nil
	}

	worker := controller.NewWorker(st, "w-test", execute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = worker.Run(ctx); close(done) }()

	testutil.Eventually(t, 5*time.Second, func() bool {
		var status, outcome, owner string
		err := pool.QueryRow(t.Context(),
			`SELECT status, outcome, lease_owner FROM runs WHERE id=$1`, runID,
		).Scan(&status, &outcome, &owner)
		if err != nil {
			return false
		}
		return status == "COMPLETED" && outcome == "ANSWER_READY" && owner == "w-test"
	}, "run 应被领取并完成")

	if calls.Load() != 1 {
		t.Fatalf("钩子应被调用 1 次，实际 %d", calls.Load())
	}

	// 优雅停机：取消后 Run 应很快返回（领取循环与心跳 goroutine 都要退出）
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 worker 未在 2s 内退出")
	}
}

// TestWorkerIdempotent：队列空转时不领取、不重复执行已完成的 run。
func TestWorkerIdempotent(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	runID := testutil.SeedRun(t, pool, "idem")

	st := testStore(t, dbURL)

	var calls atomic.Int32
	worker := controller.NewWorker(st, "w-idem", func(ctx context.Context, c controller.Claim) (string, error) {
		calls.Add(1)
		return "ANSWER_READY", nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	var epoch int64
	testutil.Eventually(t, 5*time.Second, func() bool {
		var status string
		if err := pool.QueryRow(t.Context(),
			`SELECT status, lease_epoch FROM runs WHERE id=$1`, runID,
		).Scan(&status, &epoch); err != nil {
			return false
		}
		return status == "COMPLETED"
	}, "run 应完成")

	// 再空转一个轮询周期：已完成 run 不该被二次执行。
	// 断言 epoch==1（恰好被领取一次）——能抓到「ClaimRun 的 WHERE 被改坏
	// 导致 COMPLETED 可再领」这类回归，比只数钩子调用更硬。
	time.Sleep(3 * time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("已完成 run 不应被重复执行，钩子被调 %d 次", got)
	}
	if epoch != 1 {
		t.Fatalf("run 应恰好被领取一次（epoch=1），实际 %d", epoch)
	}
}
