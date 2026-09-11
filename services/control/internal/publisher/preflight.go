package publisher

// preflight.go（T022）：发布前重核（FR-8）。
// 批准（AC32）到执行之间有时间窗，这扇窗里世界会变：
// Issue 关了、来了新的人类回复、草稿被新分析取代——任何一项发生，
// 这条审批就不再是操作者当年批准的那个承诺，必须作废而不是照发。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// preflight 失败原因（进 EXPIRED 说明与日志，人类可读）。
var (
	ErrIssueClosed    = errors.New("发布前重核失败：Issue 已关闭")
	ErrNewHumanReply  = errors.New("发布前重核失败：批准后出现了新的人类回复")
	ErrDraftStale     = errors.New("发布前重核失败：草稿已被新分析取代")
	ErrBodyMismatch   = errors.New("发布前重核失败：草稿内容与审批锚不一致")
	ErrPreflightInfra = errors.New("发布前重核暂时无法完成（基础设施错误，未核实）")
)

// preflightClockMargin：GitHub 评论时间戳秒级精度、DB 微秒级，且两端服务器
// 存在时钟偏差——把 Since 往前拨 2s，边界附近的评论宁可误判为「新」
// （保守方向：多拦一次重发的代价只是人工再批一次）。
const preflightClockMargin = 2 * time.Second

// Preflight 在真实调用 GitHub 之前执行，任一失败返回说明。
// bundle 与 draft 由调用方查好传入（发布流程还要用它们，避免重复查询）。
//
// 检查项（从宽松到严格）：
//  1. Issue 状态必须是 open——closed 的 Issue 发评论没有读者；
//  2. bundle 创建之后不得有人类新回复——操作者批准的是"当时的上下文"，
//     有人类插话意味着上下文已变；bot 的回复（[bot] 后缀，含我们自己）
//     不在此列。时间过滤交给 GitHub 服务端（Since 参数），
//     不受分页影响（review P1-3：>100 评论时客户端只看第 1 页会漏判）；
//  3. 草稿必须仍是 current 且正文哈希与审批锚一致（AC33 的执行时复验）。
//
// 错误二分（review P1-5）：违反约束（ErrIssueClosed 等哨兵）= 核实了且不满足，
// 调用方应作废审批；ErrPreflightInfra = 没核实成（网络/5xx），调用方必须
// 保持现状可重试——误杀一个已批准的包和漏发一样是事故。
func Preflight(ctx context.Context, ops GitHubOps, issueNumber int64, b db.ApprovalBundle, draft db.ReplyDraft, body []byte) error {
	issue, err := ops.GetIssue(ctx, issueNumber)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightInfra, err)
	}
	if issue.State != "open" {
		return ErrIssueClosed
	}

	since := b.CreatedAt.Time.Add(-preflightClockMargin)
	comments, err := ops.ListComments(ctx, issueNumber, ListOpts{Since: &since})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightInfra, err)
	}
	for _, c := range comments {
		if !strings.HasSuffix(c.Author, "[bot]") {
			return ErrNewHumanReply
		}
	}

	if draft.Status != "current" {
		return ErrDraftStale
	}
	if b.ContentDigest != draft.BodyDigest || len(body) == 0 {
		return ErrBodyMismatch
	}
	return nil
}
