package controller

// 租约协议 API（AC23–AC26）：领取与心跳在 worker.go 的循环里，
// 本文件收口「提交」与「取消检查」——协议的另外两个动作。
// 集中在 lease.go 的原因：提交协议是全系统数据一致性的心脏，
// 它的每条规则都要能在一个文件里被读完、被测到。

import (
	"context"
	"errors"

	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// ErrStaleEpoch：旧纪元写回被拒（AC24）。持有者应放弃一切后续动作。
var ErrStaleEpoch = errors.New("租约已失效：旧纪元写回被拒")

// ErrPayloadConflict：同 commit_id 不同内容（AC26）。调用方语义是 409。
var ErrPayloadConflict = errors.New("同 commit_id 但 payload_hash 不一致")

// CommitOutcome 描述一次提交的协议结果。
type CommitOutcome int

const (
	CommitNew        CommitOutcome = iota // 首次提交：回执+终态落库成功
	CommitDuplicated                      // 重放：同 id 同 hash，幂等回显（AC25）
)

// CommitRunResult 提交一次执行的产出（回执 + 终态），整个协议在一个事务里。
//
// 协议规则：
//   - 首次提交：插入 run_commits 回执 + CompleteRun（owner+epoch 双身份校验）。
//     同事务保证「有回执必有终态」——不存在回执落了但终态写回被拒的半提交，
//     僵尸留下的只有回滚，不是脏数据；
//   - 重放（同 run_id+commit_id 再次提交）：比对 payload_hash——
//     相同 = 网络重试，幂等返回 CommitDuplicated 不再动终态（AC25）；
//     不同 = 同 ID 不同内容，数据矛盾，返回 ErrPayloadConflict（AC26），
//     整个事务回滚（包括对终态的任何修改）；
//   - 终态写回 0 行（owner/epoch 不匹配）：ErrStaleEpoch，事务回滚（AC24）。
//
// outcome 直接写 runs.outcome（ANSWER_READY 等），result 是完整结构化结果
// （run-commit.schema.json 的镜像），存进回执供取证与前端展示。
func CommitRunResult(
	ctx context.Context,
	st *store.Store,
	owner string,
	claim Claim,
	commitID string,
	payloadHash string,
	result []byte,
	outcome string,
) (CommitOutcome, error) {
	var oc CommitOutcome

	err := st.WithTx(ctx, func(q *db.Queries) error {
		_, err := q.InsertRunCommit(ctx, db.InsertRunCommitParams{
			RunID:       claim.ID,
			CommitID:    commitID,
			PayloadHash: payloadHash,
			LeaseEpoch:  claim.LeaseEpoch,
			Stage:       "issue-analysis",
			Result:      result,
			Seq:         1, // M1 单阶段，固定 1；多阶段执行流（M2）再启用递增
		})
		switch {
		case errors.Is(err, store.ErrNoRows):
			// ON CONFLICT DO NOTHING → 回执已存在：比对内容
			existing, e := q.GetRunCommitPayloadHash(ctx, db.GetRunCommitPayloadHashParams{
				RunID: claim.ID, CommitID: commitID,
			})
			if e != nil {
				return e
			}
			if existing != payloadHash {
				return ErrPayloadConflict
			}
			oc = CommitDuplicated // 重放：终态早已落库，什么都不改
			return nil
		case err != nil:
			return err
		}

		rows, e := q.CompleteRun(ctx, db.CompleteRunParams{
			ID: claim.ID, Outcome: &outcome, LeaseOwner: &owner, LeaseEpoch: claim.LeaseEpoch,
		})
		if e != nil {
			return e
		}
		if rows == 0 {
			return ErrStaleEpoch // 僵尸写回：回滚回执，什么都别留
		}
		oc = CommitNew
		return nil
	})
	if err != nil {
		return 0, err
	}
	return oc, nil
}

// CancelRequested 是执行方的取消检查钩子：在两个长步骤之间调用，
// 发现 CANCEL_REQUESTED 就尽早收手（终态交给取消方或清道夫写）。
// 查不到 run（已被删等）按「未取消」处理——取消检查永远不该比执行本身更致命。
func CancelRequested(ctx context.Context, st *store.Store, runID string) (bool, error) {
	status, err := st.GetRunStatus(ctx, runID)
	if errors.Is(err, store.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "CANCEL_REQUESTED", nil
}
