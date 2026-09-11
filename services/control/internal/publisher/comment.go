package publisher

// comment.go（T023/T024）：评论发布、回执与响应丢失核对。
//
// 铁律（AC36）：结果不确定时只能「核对」，绝不自动重发——
// 发出去的第二条评论是收不回的；RECONCILING 查无实据时转人工。
//
// 重入语义（review P1-1）：EXECUTING 不是终态而是「可能中断的进行时」。
// 发布链在抢占 bundle→EXECUTING 后的任何一步崩溃，重启后的 Publish 重入时
// 按 action 状态续办（见 resume 与 dispatchAction），不存在卡死的 EXECUTING。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	githubpkg "github.com/google/go-github/v90/github"

	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// 发布域错误。
var (
	ErrBundleNotApproved   = errors.New("审批包不在可发布状态")
	ErrActionAlreadyMoved  = errors.New("动作已有终态或正在处理，回显当前状态")
	ErrNeedsManualDecision = errors.New("核对未命中：需要人工决策（绝不自动重发）")
)

// Publisher 编排发布链：重核 → 发布 → 回执/核对。
type Publisher struct {
	st       *store.Store
	resolver ClientResolver
	dir      string // artifacts 目录（草稿正文从 content-addressable 文件读出）
}

func NewPublisher(st *store.Store, resolver ClientResolver, artifactDir string) *Publisher {
	return &Publisher{st: st, resolver: resolver, dir: artifactDir}
}

// Publish 对 APPROVED 的审批包执行发布；对中断的发布做恢复续办。
//
// 幂等与恢复分发：
//   - bundle=EXECUTED / action 终态 → 回显（ErrActionAlreadyMoved）；
//   - bundle=EXECUTING（上次中断）→ 按 action 状态续办：未执行的继续执行，
//     执行过但结果未知的（EXECUTING/RECONCILING）先核对（AC36）。
func (p *Publisher) Publish(ctx context.Context, bundleID string) (db.Action, error) {
	b, err := p.st.GetApprovalBundle(ctx, bundleID)
	if err != nil {
		return db.Action{}, err
	}

	switch b.Status {
	case "APPROVED":
		// 抢占式推进：并发触发只允许一个进入发布（0 行 = 别人已推进）
		if rows, err := p.st.UpdateBundleStatus(ctx, db.UpdateBundleStatusParams{
			ID: bundleID, Status: "EXECUTING", Status_2: "APPROVED",
		}); err != nil {
			return db.Action{}, err
		} else if rows == 0 {
			if handled, act, err := p.resume(ctx, bundleID); handled {
				return act, err
			}
		}
	case "EXECUTING":
		if handled, act, err := p.resume(ctx, bundleID); handled {
			return act, err
		}
	case "EXECUTED":
		act, getErr := p.st.GetActionByBundle(ctx, bundleID)
		if getErr != nil && !errors.Is(getErr, store.ErrNoRows) {
			return db.Action{}, getErr
		}
		return act, ErrActionAlreadyMoved
	default:
		return db.Action{}, fmt.Errorf("%w: status=%s", ErrBundleNotApproved, b.Status)
	}

	action, err := p.getOrCreateAction(ctx, bundleID)
	if err != nil {
		return db.Action{}, err
	}
	if d := p.dispatchAction(ctx, action); d.handled {
		return d.action, d.err
	}
	// PENDING → EXECUTING 抢占（0 行 = 并发推进，读最新状态回显）
	if rows, err := p.st.MarkActionExecuting(ctx, action.ID); err != nil {
		return db.Action{}, err
	} else if rows == 0 {
		return p.st.GetAction(ctx, action.ID)
	}

	// 组装执行上下文
	draft, err := p.st.GetReplyDraft(ctx, b.DraftID)
	if err != nil {
		return db.Action{}, fmt.Errorf("读取草稿失败: %w", err)
	}
	body, err := p.readArtifact(b.ContentDigest)
	if err != nil {
		return db.Action{}, fmt.Errorf("读取草稿正文失败: %w", err)
	}
	var target struct {
		RepoNumericID int64 `json:"repo_numeric_id"`
		IssueNumber   int64 `json:"issue_number"`
	}
	if err := json.Unmarshal(b.Target, &target); err != nil {
		return db.Action{}, fmt.Errorf("解析 target 失败: %w", err)
	}

	rc, err := p.resolver.ForRepo(ctx, target.RepoNumericID)
	if err != nil {
		// 基础设施错误（review P1-5）：从未触碰 GitHub——action 回退 PENDING，
		// 重入 Publish 直接继续执行而不是误入核对。
		if rows, rErr := p.st.ResetActionToPending(ctx, action.ID); rErr == nil && rows > 0 {
			action.Status = "PENDING"
		}
		return action, fmt.Errorf("解析 GitHub 客户端失败（可重试）: %w", err)
	}

	// T022：发布前重核——违反约束就地作废；核实不了回退可重试。
	if err := Preflight(ctx, rc.Ops, target.IssueNumber, b, draft, body); err != nil {
		if errors.Is(err, ErrPreflightInfra) {
			if rows, rErr := p.st.ResetActionToPending(ctx, action.ID); rErr == nil && rows > 0 {
				action.Status = "PENDING"
			}
			return action, err
		}
		_, _ = p.st.ExpireBundle(ctx, bundleID)
		return db.Action{}, p.failDefinitive(ctx, action, err)
	}

	// T023：发布
	commentID, commentURL, err := rc.Ops.CreateComment(ctx, target.IssueNumber, string(body))
	if err == nil {
		return p.succeed(ctx, action, b.ID, commentID, commentURL)
	}

	// T024：错误分类——只有「结果不确定」才进核对；确定失败就地 FAILED
	if isDefinitive(err) {
		return db.Action{}, p.failDefinitive(ctx, action, fmt.Errorf("GitHub 明确拒绝: %w", err))
	}
	slog.Warn("发布结果不确定，进入核对", "action", action.ID, "err", err)
	if rows, err := p.st.MarkActionReconciling(ctx, action.ID); err != nil {
		return db.Action{}, err
	} else if rows == 0 {
		return p.st.GetAction(ctx, action.ID)
	}
	return p.Reconcile(ctx, action.ID)
}

// resume 是 EXECUTING/EXECUTED 的重入入口。
// 返回 handled=true 表示调用方应直接返回 (action, err)；
// handled=false 表示「继续主流程执行」（无 action 的崩溃窗口，或 PENDING）。
func (p *Publisher) resume(ctx context.Context, bundleID string) (bool, db.Action, error) {
	action, err := p.st.GetActionByBundle(ctx, bundleID)
	if errors.Is(err, store.ErrNoRows) {
		// 崩溃窗口 1：bundle 已 EXECUTING 但 action 未建——从头续办
		return false, db.Action{}, nil
	}
	if err != nil {
		return true, db.Action{}, err
	}
	if d := p.dispatchAction(ctx, action); d.handled {
		return true, d.action, d.err
	}
	// 崩溃窗口 2：action 建了但未执行（PENDING）——继续主流程
	return false, action, nil
}

// dispatchResult 让 dispatchAction 能表达「未处理，继续主流程」。
type dispatchResult struct {
	handled bool
	action  db.Action
	err     error
}

// dispatchAction 按 action 状态分发（恢复语义核心，review P1-1）：
//   - SUCCEEDED/FAILED → 回显；
//   - EXECUTING（上次执行到一半崩溃，结果未知）→ 先核对再决定；
//   - RECONCILING → 核对；
//   - PENDING → 未处理（handled=false），让主流程执行。
func (p *Publisher) dispatchAction(ctx context.Context, action db.Action) dispatchResult {
	switch action.Status {
	case "SUCCEEDED", "FAILED":
		return dispatchResult{handled: true, action: action, err: ErrActionAlreadyMoved}
	case "EXECUTING", "RECONCILING":
		if action.Status == "EXECUTING" {
			slog.Warn("发现中断的 EXECUTING action：结果未知，先核对", "action", action.ID)
		}
		act, err := p.Reconcile(ctx, action.ID)
		return dispatchResult{handled: true, action: act, err: err}
	default: // PENDING
		return dispatchResult{handled: false}
	}
}

// Reconcile（T024）：按「目标 + bot 身份 + 内容」拉取远端评论核对。
// 命中 → SUCCEEDED 并补记 remote 锚点；未命中 → 保持 RECONCILING，
// 返回 ErrNeedsManualDecision——是否重发由人工决定，系统绝不代劳。
//
// 核对键（分布式协议调研结论加固）：只匹配 [bot] 账号发出的评论 + 全文一致。
// 人类恰好写出逐字相同的正文几乎不可能；[bot] 限定把误命中收窄到机器人
// 产物。GitHub 读延迟下刚发的评论可能暂不可见——查无实据不是永久结论，
// 操作者可稍后经 reconcile 端点再核一次（review P1-2 的观察窗语义）。
func (p *Publisher) Reconcile(ctx context.Context, actionID string) (db.Action, error) {
	action, err := p.st.GetAction(ctx, actionID)
	if err != nil {
		return db.Action{}, err
	}
	switch action.Status {
	case "SUCCEEDED":
		return action, nil
	case "FAILED", "PENDING":
		// FAILED：终态；PENDING：从未执行，无「结果未知」可核
		return action, ErrActionAlreadyMoved
	case "EXECUTING":
		// 从「执行中被打断」进入核对态（0 行=已被并发推进，读最新状态）
		if rows, err := p.st.MarkActionReconciling(ctx, actionID); err != nil {
			return db.Action{}, err
		} else if rows > 0 {
			action.Status = "RECONCILING"
		}
	}

	b, err := p.st.GetApprovalBundle(ctx, action.BundleID)
	if err != nil {
		return db.Action{}, err
	}
	if _, err := p.st.GetReplyDraft(ctx, b.DraftID); err != nil {
		return db.Action{}, err // 草稿被删属异常，转人工
	}
	body, err := p.readArtifact(b.ContentDigest)
	if err != nil {
		return db.Action{}, err
	}
	var target struct {
		RepoNumericID int64 `json:"repo_numeric_id"`
		IssueNumber   int64 `json:"issue_number"`
	}
	if err := json.Unmarshal(b.Target, &target); err != nil {
		return db.Action{}, err
	}

	rc, err := p.resolver.ForRepo(ctx, target.RepoNumericID)
	if err != nil {
		return db.Action{}, fmt.Errorf("解析 GitHub 客户端失败: %w", err)
	}
	// Desc：新评论在前，第 1 页即覆盖刚发出的评论（review P1-3）；
	// Since 用 action 创建时间收窄扫描窗口。
	since := action.CreatedAt.Time.Add(-preflightClockMargin)
	comments, err := rc.Ops.ListComments(ctx, target.IssueNumber, ListOpts{Since: &since, Desc: true})
	if err != nil {
		// 核对自身失败：保持 RECONCILING（不动状态承诺），下次再核
		return action, fmt.Errorf("核对失败（保持 RECONCILING，可重试）: %w", err)
	}
	for _, c := range comments {
		if strings.HasSuffix(c.Author, "[bot]") && c.Body == string(body) {
			slog.Info("核对命中：评论已在远端", "action", actionID, "remote_id", c.ID)
			return p.succeed(ctx, action, b.ID, c.ID, c.URL)
		}
	}
	slog.Warn("核对未命中，转人工决策", "action", actionID)
	return action, ErrNeedsManualDecision
}

// ---- 内部步骤 ----

// getOrCreateAction 取或建 bundle 的 action（bundle:action 恒 1:1，
// 库级 UNIQUE(actions.bundle_id) 兜底，并发插入靠 ON CONFLICT 回读）。
func (p *Publisher) getOrCreateAction(ctx context.Context, bundleID string) (db.Action, error) {
	act, err := p.st.GetActionByBundle(ctx, bundleID)
	if err == nil {
		return act, nil
	}
	if !errors.Is(err, store.ErrNoRows) {
		return db.Action{}, err
	}
	created, err := p.st.InsertAction(ctx, db.InsertActionParams{ID: ids.New("action"), BundleID: bundleID})
	if errors.Is(err, store.ErrNoRows) {
		return p.st.GetActionByBundle(ctx, bundleID) // ON CONFLICT 落空 = 并发已建
	}
	return created, err
}

func (p *Publisher) succeed(ctx context.Context, action db.Action, bundleID string, commentID int64, url string) (db.Action, error) {
	receipt, _ := json.Marshal(map[string]any{
		"action_id":         action.ID,
		"bundle_id":         bundleID,
		"status":            "SUCCEEDED",
		"remote_comment_id": commentID,
		"remote_url":        url,
		"checked_at":        time.Now().UTC().Format(time.RFC3339),
	})
	rows, err := p.st.MarkActionSucceeded(ctx, db.MarkActionSucceededParams{
		ID: action.ID, RemoteCommentID: &commentID, RemoteUrl: &url,
		Receipt: receipt,
	})
	if err != nil {
		return db.Action{}, err
	}
	if rows == 0 {
		return p.st.GetAction(ctx, action.ID)
	}
	_, _ = p.st.UpdateBundleStatus(ctx, db.UpdateBundleStatusParams{
		ID: bundleID, Status: "EXECUTED", Status_2: "EXECUTING",
	})
	return p.st.GetAction(ctx, action.ID)
}

// failDefinitive：确定失败——action FAILED + 原因入回执 + bundle 同步 EXPIRED
// （review P2-2：EXPIRED 同样堵死重发暗门，且不留 EXECUTING 僵尸视图）。
// err 用 %w 包装上抛：ErrIssueClosed 等哨兵必须能被调用方 errors.Is 识别。
func (p *Publisher) failDefinitive(ctx context.Context, action db.Action, err error) error {
	note := err.Error()
	receipt, _ := json.Marshal(map[string]any{
		"action_id":  action.ID,
		"bundle_id":  action.BundleID,
		"status":     "FAILED",
		"checked_at": time.Now().UTC().Format(time.RFC3339),
		"note":       note,
	})
	if _, err := p.st.MarkActionFailed(ctx, db.MarkActionFailedParams{
		ID: action.ID, Receipt: receipt,
	}); err != nil {
		slog.Error("标记 FAILED 失败", "action", action.ID, "err", err)
	}
	_, _ = p.st.ExpireBundle(ctx, action.BundleID)
	return fmt.Errorf("%w（action=%s）", err, action.ID)
}

// readArtifact 按 digest 读 content-addressable 文件（草稿正文）。
func (p *Publisher) readArtifact(digest string) ([]byte, error) {
	if len(digest) < 2 {
		return nil, fmt.Errorf("非法 digest: %q", digest)
	}
	return os.ReadFile(filepath.Join(p.dir, digest[:2], digest))
}

// isDefinitive 区分「GitHub 明确拒绝」（重试无意义）与「结果不确定」
// （可能已生效）。判据：go-github 的 ErrorResponse 带 HTTP 状态。
// 408/429 排除（review P1-4）：请求未被处理、无副作用，属可重试临时态——
// 一次限流不该把已批准的 bundle 永久杀死。
func isDefinitive(err error) bool {
	var ghErr *githubpkg.ErrorResponse
	if errors.As(err, &ghErr) {
		if ghErr.Response == nil {
			return false
		}
		switch code := ghErr.Response.StatusCode; {
		case code == 408, code == 429:
			return false
		case code >= 400 && code < 500:
			return true
		}
	}
	return false
}
