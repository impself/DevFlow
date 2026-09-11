package controller

// approval.go（T020/T021）：审批包的生成、批准与拒绝。
// 宪法 II 的落地文件：人批准的是「精确目标 + 精确内容」的绑定，
// 任一漂移都让审批失效；重复操作幂等回显。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// 审批有效期（PRD §15.2）：批准窗口 24 小时，过时必须重新走分析-审批流。
const bundleTTL = 24 * time.Hour

// 审批域错误：HTTP 语义由 handler 翻译（409/404）。
var (
	ErrNoDraft        = errors.New("case 没有可审批的 current 草稿")
	ErrNotApprovable  = errors.New("NEEDS_INFO 草稿没有可发布内容，不可审批")
	ErrBundleExpired  = errors.New("审批包已过期或内容已变化")
	ErrBundleNotState = errors.New("审批包状态不允许该操作")
)

// ApprovalService 是审批域的服务对象（生成/批准/拒绝）。
type ApprovalService struct {
	st *store.Store
}

func NewApprovalService(st *store.Store) *ApprovalService {
	return &ApprovalService{st: st}
}

// CreateBundle 从 case 的 current 草稿生成审批包。
//
// 绑定三元组（宪法 II）：target（repo_numeric_id+issue_number，不用可改名的
// owner/name）+ content_digest（= 草稿 body_digest）+ 24h 时效。
// idempotency_key 从 (case, draft内容) 结构派生：同一草稿重复创建返回同一包
// ——与 commit_id 同一个设计原则，幂等键来自业务结构。
func (s *ApprovalService) CreateBundle(ctx context.Context, caseID string) (db.ApprovalBundle, error) {
	draft, err := s.st.GetCurrentDraftForCase(ctx, caseID)
	if errors.Is(err, store.ErrNoRows) {
		return db.ApprovalBundle{}, ErrNoDraft
	}
	if err != nil {
		return db.ApprovalBundle{}, err
	}
	if draft.Conclusion != "ANSWER_READY" {
		return db.ApprovalBundle{}, ErrNotApprovable
	}

	cs, err := s.st.GetCase(ctx, caseID)
	if err != nil {
		return db.ApprovalBundle{}, fmt.Errorf("读取 case: %w", err)
	}
	repo, err := s.st.GetRepository(ctx, cs.RepoID)
	if err != nil {
		return db.ApprovalBundle{}, fmt.Errorf("读取 repository: %w", err)
	}

	target, err := json.Marshal(map[string]any{
		"repo_numeric_id": repo.RepoNumericID,
		"issue_number":    cs.IssueNumber,
	})
	if err != nil {
		return db.ApprovalBundle{}, err
	}

	key := fmt.Sprintf("bundle-%s-%s", caseID, draft.BodyDigest[:16])
	created, err := s.st.InsertApprovalBundle(ctx, db.InsertApprovalBundleParams{
		ID:             ids.New("bundle"),
		CaseID:         caseID,
		DraftID:        draft.ID,
		ActionType:     "POST_ISSUE_COMMENT", // M1 唯一对外动作（宪法 I）
		Target:         target,
		ContentDigest:  draft.BodyDigest,
		ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(bundleTTL), Valid: true},
		IdempotencyKey: key,
	})
	if errors.Is(err, store.ErrNoRows) {
		// 同 key 已存在（重复点击）：回显原包（AC35）
		return s.st.GetApprovalBundleByIdempotencyKey(ctx, key)
	}
	if err != nil {
		return db.ApprovalBundle{}, err
	}
	return created, nil
}

// Approve 批准一个 PENDING 的审批包。
// 返回 (更新后的 bundle, nil)。错误语义：
//   - ErrBundleExpired：已过 24h 或草稿内容/状态漂移（AC33）——同时把包翻 EXPIRED；
//   - ErrBundleNotState：包已在 APPROVED 及之后的状态——这是重复批准，
//     直接回显当前状态（AC35），调用方按 200 处理。
func (s *ApprovalService) Approve(ctx context.Context, bundleID, operator string) (db.ApprovalBundle, error) {
	b, err := s.st.GetApprovalBundle(ctx, bundleID)
	if err != nil {
		return db.ApprovalBundle{}, err
	}

	// AC35：终态或已批准——回显原结果，不做任何写
	switch b.Status {
	case "APPROVED", "EXECUTING", "EXECUTED":
		return b, nil
	case "REJECTED", "EXPIRED":
		return b, ErrBundleNotState
	}

	// AC33 前置核对：内容锚必须仍然成立（草稿还在且是 current 且 digest 一致）
	draft, err := s.st.GetReplyDraft(ctx, b.DraftID)
	if err != nil {
		return db.ApprovalBundle{}, err
	}
	if draft.Status != "current" || draft.BodyDigest != b.ContentDigest {
		if _, err := s.st.ExpireBundle(ctx, bundleID); err != nil {
			return db.ApprovalBundle{}, err
		}
		return db.ApprovalBundle{}, ErrBundleExpired
	}

	rows, err := s.st.ApproveBundle(ctx, db.ApproveBundleParams{
		ID: bundleID, ApprovedBy: &operator,
	})
	if err != nil {
		return db.ApprovalBundle{}, err
	}
	if rows == 0 {
		// 并发窗口：核对通过后、UPDATE 前被他人改态或到达 expires_at
		return db.ApprovalBundle{}, ErrBundleNotState
	}
	return s.st.GetApprovalBundle(ctx, bundleID)
}

// Reject 拒绝 PENDING 的审批包；终态包拒绝重复改写。
func (s *ApprovalService) Reject(ctx context.Context, bundleID string) (db.ApprovalBundle, error) {
	rows, err := s.st.RejectBundle(ctx, bundleID)
	if err != nil {
		return db.ApprovalBundle{}, err
	}
	if rows == 0 {
		b, getErr := s.st.GetApprovalBundle(ctx, bundleID)
		if getErr != nil {
			return db.ApprovalBundle{}, getErr
		}
		return b, ErrBundleNotState
	}
	return s.st.GetApprovalBundle(ctx, bundleID)
}
