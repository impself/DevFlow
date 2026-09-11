// Package publisher 执行「已批准的外部动作」：发布前重核（preflight）、
// 评论发布与回执、响应丢失核对（reconcile）。
// 它是全系统唯一允许写 GitHub 的包（宪法 I：对外动作仅 POST_ISSUE_COMMENT）。
package publisher

import (
	"context"
	"time"
)

// IssueInfo 是 preflight 需要的 Issue 投影。
type IssueInfo struct {
	State string // "open" | "closed"
}

// Comment 是 Issue 评论的最小投影。
type Comment struct {
	ID        int64
	URL       string
	Author    string
	Body      string
	CreatedAt time.Time
}

// ListOpts 是拉评论的服务端过滤参数（review P1-3：分页正确性的根基）。
// Since：只返回此时间之后的评论——preflight 用它把「批准后新回复」检查
// 完全交给服务端，绕开分页；Desc：新评论在前——reconcile 用它保证
// 第 1 页就能看到刚发出的评论，不被「最旧 100 条」淹没。
type ListOpts struct {
	Since *time.Time
	Desc  bool
}

// GitHubOps 是发布链对一个「已解析身份」客户端的能力面。
// 已解析 = 已按 installation 换好 token，且知道 owner/name。
type GitHubOps interface {
	GetIssue(ctx context.Context, number int64) (*IssueInfo, error)
	ListComments(ctx context.Context, number int64, opts ListOpts) ([]Comment, error)
	CreateComment(ctx context.Context, number int64, body string) (id int64, url string, err error)
}

// ResolvedClient 是一次解析的结果：目标仓库全名 + 具备身份的客户端。
type ResolvedClient struct {
	Owner string
	Name  string
	Ops   GitHubOps
}

// ClientResolver 按仓库数字 ID 解析出可用的 GitHub 客户端。
// 数字 ID 是 repositories 表的锚点（owner/name 可改名）；installation 换 token
// 的细节被封在实现里，发布逻辑只面对 GitHubOps。
type ClientResolver interface {
	ForRepo(ctx context.Context, repoNumericID int64) (*ResolvedClient, error)
}
