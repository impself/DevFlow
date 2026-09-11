package controller_test

// 租约协议的并发集成测试（T019，AC23–AC26）。
// 提交协议的语义测试在 lease_test.go；这里专攻「并发」与「时间」两个维度：
//   - 并发领取互斥：SKIP LOCKED 在真实多 goroutine 下不重复发任务；
//   - 心跳续租：续租真的延长了服务端租约；
//   - 过期接管全链路：过期 → 清道夫 → 接管 → 旧执有者心跳失权 + 提交被拒。
//
// 说明：tasks.md 原定路径 tests/control/（模块外），但 internal/ 包不允许被
// 模块外导入，故落在 internal/controller/ 与其余协议测试同包。

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// TestConcurrentClaimMutualExclusion：16 个 worker 抢 8 条 run，恰好 8 次成功、
// 每条 run 恰好被领一次（AC23 的领取侧）。
func TestConcurrentClaimMutualExclusion(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)

	const totalRuns = 8
	const workers = 16
	for range totalRuns {
		testutil.SeedRun(t, pool, "conc")
	}

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	var success atomic.Int32
	var mu sync.Mutex
	claimedBy := map[string]string{} // runID → owner（检测重复领取）

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			owner := "worker-" + string(rune('a'+w))
			for {
				claim, err := st.ClaimRun(t.Context(), &owner)
				if errors.Is(err, store.ErrNoRows) {
					return // 队列空，收工
				}
				if err != nil {
					t.Errorf("领取错误: %v", err)
					return
				}
				success.Add(1)
				mu.Lock()
				if prev, dup := claimedBy[claim.ID]; dup {
					t.Errorf("run %s 被重复领取: %s 和 %s", claim.ID, prev, owner)
				}
				claimedBy[claim.ID] = owner
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if got := success.Load(); got != totalRuns {
		t.Fatalf("应恰好成功领取 %d 次，得到 %d", totalRuns, got)
	}
}

// TestHeartbeatExtendsLease：心跳后服务端租约时间必须前进。
func TestHeartbeatExtendsLease(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	testutil.SeedRun(t, pool, "hb")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	owner := "worker-hb"
	claim, err := st.ClaimRun(t.Context(), &owner)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}

	readExpiry := func() time.Time {
		t.Helper()
		var exp time.Time
		if err := pool.QueryRow(t.Context(),
			`SELECT lease_expires_at FROM runs WHERE id=$1`, claim.ID).Scan(&exp); err != nil {
			t.Fatalf("读租约: %v", err)
		}
		return exp
	}

	before := readExpiry()
	time.Sleep(1100 * time.Millisecond) // 让 clock 真实前进，排除同刻比较的假阳性
	hb := db.HeartbeatRunParams{ID: claim.ID, LeaseOwner: &owner, LeaseEpoch: claim.LeaseEpoch}
	rows, err := st.HeartbeatRun(t.Context(), hb)
	if err != nil || rows != 1 {
		t.Fatalf("心跳应命中 1 行: rows=%d err=%v", rows, err)
	}
	after := readExpiry()

	if !after.After(before) {
		t.Fatalf("心跳后租约应前进: before=%v after=%v", before, after)
	}
	// 租约 ≈ now+30s：给 10s 容差防慢环境
	if d := time.Until(after); d < 20*time.Second || d > 40*time.Second {
		t.Fatalf("租约剩余应约 30s，得到 %v", d)
	}
}

// TestLeaseTakeoverFullChain：过期 → 清道夫 → 接管 → 旧执有者双重失权。
func TestLeaseTakeoverFullChain(t *testing.T) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	testutil.SeedRun(t, pool, "takeover")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()

	ownerA := "worker-a"
	claimA, err := st.ClaimRun(ctx, &ownerA)
	if err != nil {
		t.Fatalf("A 领取: %v", err)
	}

	// 手动把租约拨到过去，触发清道夫
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET lease_expires_at = now() - interval '1s' WHERE id=$1`, claimA.ID); err != nil {
		t.Fatalf("制造过期: %v", err)
	}
	if rows, err := st.ExpireStaleRuns(ctx); err != nil || rows != 1 {
		t.Fatalf("清道夫应翻 1 行: rows=%d err=%v", rows, err)
	}

	// B 接管：epoch +1，owner 换人
	ownerB := "worker-b"
	claimB, err := st.ClaimRun(ctx, &ownerB)
	if err != nil {
		t.Fatalf("B 接管: %v", err)
	}
	if claimB.ID != claimA.ID {
		t.Fatalf("B 应接管同一条 run: %s vs %s", claimB.ID, claimA.ID)
	}
	if claimB.LeaseEpoch != claimA.LeaseEpoch+1 {
		t.Fatalf("接管应使 epoch+1: %d vs %d", claimB.LeaseEpoch, claimA.LeaseEpoch)
	}

	// 旧执有者 A 的心跳必须 0 行（失权信号，PRD §11.3）
	hbA := db.HeartbeatRunParams{ID: claimA.ID, LeaseOwner: &ownerA, LeaseEpoch: claimA.LeaseEpoch}
	rows, err := st.HeartbeatRun(ctx, hbA)
	if err != nil || rows != 0 {
		t.Fatalf("A 的心跳应 0 行: rows=%d err=%v", rows, err)
	}

	// A 拿旧纪元提交必须被拒（AC24）
	if _, err := controller.CommitRunResult(ctx, st, ownerA, claimA,
		"issue-analysis-"+claimA.ID, "hash-a", []byte(`{}`), "ANSWER_READY"); err == nil {
		t.Fatal("A 的旧纪元提交应被拒")
	}

	// B 用新纪元提交成功（AC23），且回执里记的是 B 的 epoch
	oc, err := controller.CommitRunResult(ctx, st, ownerB, claimB,
		"issue-analysis-"+claimB.ID, "hash-b", []byte(`{"ok":true}`), "ANSWER_READY")
	if err != nil || oc != controller.CommitNew {
		t.Fatalf("B 的提交应成功: %v %v", oc, err)
	}
	var receiptEpoch int64
	if err := pool.QueryRow(ctx,
		`SELECT lease_epoch FROM run_commits WHERE run_id=$1`, claimB.ID).Scan(&receiptEpoch); err != nil {
		t.Fatalf("查回执: %v", err)
	}
	if receiptEpoch != claimB.LeaseEpoch {
		t.Fatalf("回执应记 B 的 epoch=%d，得到 %d", claimB.LeaseEpoch, receiptEpoch)
	}
}
