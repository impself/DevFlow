// Package controller 承载 Case/Run 的业务状态机：领取执行（worker）、
// webhook 入口（T011）、租约协议与提交（T013/T014）。
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// 租约时序（PRD §11.3 / data-model.md）：租约 30s，心跳 5s。
// 租约可容忍约 5 拍心跳失败；失权的唯一判据是心跳影响 0 行（PRD §11.3），
// 数据库抖动（心跳报错）不算失权，下一拍再试。
// TTL 本体写死在 runs.sql 的 interval '30 seconds'，两处必须一起改。
const (
	leaseTTL             = 30 * time.Second
	heartbeatEvery       = 5 * time.Second
	heartbeatCallTimeout = 3 * time.Second // 单拍心跳上限，必须 ≤ heartbeatEvery
	claimCallTimeout     = 3 * time.Second // 领取查询上限：黑洞网络下快速失败进退避
	idlePollEvery        = 2 * time.Second
	claimBackoff         = 3 * time.Second // 数据库抖动时的重试间隔
	sweepEvery           = 15 * time.Second
	defaultMaxWorker     = 1 // M1 并发上限 1（plan.md Constraints）
)

// Claim 是一次领取的结果（直接沿用生成类型，避免镜像 struct 漂移）。
type Claim = db.ClaimRunRow

// ExecuteFunc 是 worker 的"干活"钩子：领取之后、提交之前的那段业务。
// M1 骨架用占位实现；T014 注入真实现（调 Python 分析 + 构造提交）。
// 返回 outcome 是 Run 的结论（ANSWER_READY 等），err 非 nil 走 FAILED。
type ExecuteFunc func(ctx context.Context, claim Claim) (outcome string, err error)

// Worker 从 runs 表领取任务并驱动执行，直到 ctx 取消。
type Worker struct {
	store     *store.Store
	owner     string // 领取者身份：日志、租约归属、僵尸排查全靠它
	execute   ExecuteFunc
	maxWorker int
}

func NewWorker(st *store.Store, owner string, execute ExecuteFunc) *Worker {
	return &Worker{store: st, owner: owner, execute: execute, maxWorker: defaultMaxWorker}
}

// Run 启动 worker 池并阻塞到全部 worker 退出。返回值总是 nil：
// 取消是预期退出路径，数据库抖动在循环内部消化，不该把进程带崩。
func (w *Worker) Run(ctx context.Context) error {
	done := make(chan struct{}, w.maxWorker)
	for range w.maxWorker {
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx)
		}()
	}
	// 等齐全部 worker：只等一个的话，池扩容后 Run 会提前返回、goroutine 失管。
	for range w.maxWorker {
		<-done
	}
	return nil
}

// loop 是单个 worker 的领取循环：领到就干活，没活就睡，出错就退避。
func (w *Worker) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		owner := w.owner

		// 领取查询带短超时：网络黑洞时快速失败进退避，
		// 而不是挂到 OS 级 TCP 超时（分钟级）——那会让失权检测整体失效。
		claimCtx, cancel := context.WithTimeout(ctx, claimCallTimeout)
		claim, err := w.store.ClaimRun(claimCtx, &owner)
		cancel()

		switch {
		case errors.Is(err, store.ErrNoRows):
			// 队列空：轮询间隔睡一觉。select 保证睡着时也能被取消唤醒。
			if !sleepCtx(ctx, idlePollEvery) {
				return
			}
		case err != nil:
			if ctx.Err() != nil {
				return // 停机过程中的连接错误不值得记
			}
			slog.Error("领取 run 失败，退避重试", "err", err, "owner", w.owner)
			if !sleepCtx(ctx, claimBackoff) {
				return
			}
		default:
			slog.Info("领取 run", "run", claim.ID, "case", claim.CaseID,
				"epoch", claim.LeaseEpoch, "owner", w.owner)
			w.process(ctx, claim)
		}
	}
}

// process 驱动单次执行：租约心跳 + 执行钩子 + 终态落库。
//
// 关键设计：claimCtx 独立于 ctx——心跳发现失权（0 行）时只取消 claimCtx，
// 执行钩子通过 ctx 感知并尽快收手，而 worker 主循环不受影响。
//
// 失权语义（重要）：写回 SQL 的 WHERE 只校验 owner+epoch，不校验 status——
// epoch 是唯一权威 fencing：只有接管发生（epoch+1）后旧写回才会被拒；
// 若租约仅过期（RECOVERING）但无人接管，原执行者仍是唯一干活方。
// 基于此，钩子返回时若 claimCtx 已取消，一律放弃写回：分不清"失权"与"停机"，
// 两种情况 run 都处于协议接管范围内，不需要这里再写终态。
func (w *Worker) process(ctx context.Context, claim Claim) {
	claimCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 心跳在独立 goroutine：主流程阻塞在钩子里时租约也要续命。
	// cancel 由两处触发：process 返回时的 defer（正常收尾）与心跳失权（叫停钩子）。
	go w.heartbeatLoop(claimCtx, cancel, claim)

	outcome, err := w.safeExecute(claimCtx, claim)
	if claimCtx.Err() != nil && ctx.Err() == nil {
		// 失权被踢：不写终态。若租约已被接管，写了也会被 epoch 拒；
		// 若只是过期未接管，RECOVERING 本身就是对的状态。
		slog.Warn("执行因失去租约中止", "run", claim.ID, "epoch", claim.LeaseEpoch)
		return
	}
	if ctx.Err() != nil {
		// 全局停机：不写终态，留给重启后的 Sweeper 走恢复。
		return
	}
	if err != nil {
		slog.Error("执行失败", "run", claim.ID, "err", err)
		w.markFailed(ctx, claim, truncateErr(err))
		return
	}

	rows, err := w.store.CompleteRun(ctx, db.CompleteRunParams{
		ID: claim.ID, Outcome: &outcome, LeaseOwner: &w.owner, LeaseEpoch: claim.LeaseEpoch,
	})
	switch {
	case err != nil:
		slog.Error("标记 COMPLETED 失败，尝试钉死 FAILED", "run", claim.ID, "err", err)
		// 兜底（防毒循环）：若失败是确定性的（如 outcome 撞 CHECK 枚举），
		// run 停在 RUNNING 会被 Sweeper 翻 RECOVERING 无限重跑。
		// 用同一 owner+epoch 写 FAILED 钉死它；0 行说明已被接管，静默即可。
		w.markFailed(ctx, claim, "complete 失败: "+truncateErr(err)+"; outcome="+outcome)
	case rows == 0:
		slog.Warn("COMPLETED 写回被拒（租约已易主）", "run", claim.ID, "epoch", claim.LeaseEpoch)
	default:
		slog.Info("run 完成", "run", claim.ID, "outcome", outcome, "epoch", claim.LeaseEpoch)
	}
}

// markFailed 落 FAILED 终态（带 epoch fencing），错误只记日志不上抛——
// 终态写回没有下一级重试者，失败时的唯一出路是恢复协议。
func (w *Worker) markFailed(ctx context.Context, claim Claim, errText string) {
	rows, failErr := w.store.FailRun(ctx, db.FailRunParams{
		ID: claim.ID, Error: &errText, LeaseOwner: &w.owner, LeaseEpoch: claim.LeaseEpoch,
	})
	switch {
	case failErr != nil:
		slog.Error("标记 FAILED 失败", "run", claim.ID, "err", failErr)
	case rows == 0:
		// 0 行 = epoch 已变（AC24）：旧纪元的写回被无声拒绝，这正是协议设计。
		slog.Warn("FAILED 写回被拒（租约已易主）", "run", claim.ID, "epoch", claim.LeaseEpoch)
	default:
		slog.Warn("run 标记 FAILED", "run", claim.ID, "epoch", claim.LeaseEpoch)
	}
}

// safeExecute 隔离钩子 panic：一个 run 的代码缺陷不能拖垮整个 worker 循环。
func (w *Worker) safeExecute(ctx context.Context, claim Claim) (outcome string, err error) {
	defer func() {
		if r := recover(); r != nil {
			outcome = ""
			err = fmt.Errorf("执行 panic: %v", r)
		}
	}()
	return w.execute(ctx, claim)
}

// heartbeatLoop 每 5s 续租一次；0 行受影响 = 租约已易主，cancel 叫停执行钩子。
// 单拍心跳带 3s 超时：分区（黑洞网络）时这拍按失败处理、下一拍再试，
// 绝不能让心跳本身挂死——挂死的心跳等于失权检测失效。
func (w *Worker) heartbeatLoop(ctx context.Context, cancel context.CancelFunc, claim Claim) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, callCancel := context.WithTimeout(ctx, heartbeatCallTimeout)
			rows, err := w.store.HeartbeatRun(callCtx, db.HeartbeatRunParams{
				ID: claim.ID, LeaseOwner: &w.owner, LeaseEpoch: claim.LeaseEpoch,
			})
			callCancel()
			switch {
			case err != nil:
				slog.Error("心跳失败", "run", claim.ID, "err", err) // 抖动≠失权，下拍再试
			case rows == 0:
				slog.Warn("心跳 0 行：租约已失效，取消执行", "run", claim.ID, "epoch", claim.LeaseEpoch)
				cancel()
				return
			}
		}
	}
}

// Sweeper 周期做两件事（顺序固定）：
//  1. 租约过期的 RUNNING → RECOVERING（场景 E 的恢复起点）；
//  2. 预算耗尽（attempts ≥ max）的 RECOVERING → FAILED（毒 run 终结，
//     调研改进 #6：epoch 只防双主，不防崩溃循环——预算才是循环的终止条件）。
//
// 与 Worker 分离：清道夫不需要知道谁在干活，只对状态负责。
func Sweeper(ctx context.Context, st *store.Store) error {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			rows, err := st.ExpireStaleRuns(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("清道夫扫描失败", "err", err)
				continue
			}
			if rows > 0 {
				slog.Warn("发现租约过期的 run，标记 RECOVERING", "count", rows)
			}
			if rows, err := st.FailExhaustedRuns(ctx); err != nil {
				slog.Error("毒 run 终结扫描失败", "err", err)
			} else if rows > 0 {
				slog.Warn("重试预算耗尽的 run，标记 FAILED", "count", rows)
			}
		}
	}
}

// sleepCtx 睡 d 或直到 ctx 取消；返回 false 表示被取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// truncateErr 把错误压成可入库的单行文本（error 列是 text）。
// 截断必须保 UTF-8 合法：按字节切中文会切出非法序列，PG 直接以 22021 拒收，
// 失败信息反而写不进库。切完把尾部不完整字节剥掉。
func truncateErr(err error) string {
	const max = 500
	s := err.Error()
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
