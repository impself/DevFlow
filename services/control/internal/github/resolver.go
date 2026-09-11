package github

// resolver.go：为 publisher 提供「按仓库数字 ID 解析出具备 installation 身份
// 的客户端」。解析逻辑 = repositories 表查 installation → ClientFactory 取 client。
// 凭据仍在 internal/github 内部（宪法 III），publisher 只面对解析结果。

import (
	"context"
	"fmt"

	githubpkg "github.com/google/go-github/v90/github"

	"github.com/impself/DevFlow/services/control/internal/publisher"
	"github.com/impself/DevFlow/services/control/internal/store"
)

// PublisherResolver 实现 publisher.ClientResolver。
type PublisherResolver struct {
	st      *store.Store
	factory *ClientFactory
}

func NewPublisherResolver(st *store.Store, factory *ClientFactory) *PublisherResolver {
	return &PublisherResolver{st: st, factory: factory}
}

func (r *PublisherResolver) ForRepo(ctx context.Context, repoNumericID int64) (*publisher.ResolvedClient, error) {
	repo, err := r.st.GetRepositoryByNumericID(ctx, repoNumericID)
	if err != nil {
		return nil, fmt.Errorf("查仓库 %d: %w", repoNumericID, err)
	}
	client, err := r.factory.Client(repo.InstallationID)
	if err != nil {
		return nil, err
	}
	return &publisher.ResolvedClient{
		Owner: repo.Owner,
		Name:  repo.Name,
		Ops:   &ghOps{client: client, owner: repo.Owner, name: repo.Name},
	}, nil
}

// ghOps 把 go-github 的 Issues API 适配到 publisher.GitHubOps。
type ghOps struct {
	client      *githubpkg.Client
	owner, name string
}

func (g *ghOps) GetIssue(ctx context.Context, number int64) (*publisher.IssueInfo, error) {
	iss, _, err := g.client.Issues.Get(ctx, g.owner, g.name, int(number))
	if err != nil {
		return nil, err
	}
	return &publisher.IssueInfo{State: iss.GetState()}, nil
}

func (g *ghOps) ListComments(ctx context.Context, number int64, opts publisher.ListOpts) ([]publisher.Comment, error) {
	// 单页 100 + 服务端过滤（Since）/倒序（Desc）：
	// preflight 用 Since 把「新回复」检查完全交给 GitHub，绕开分页；
	// reconcile 用 Desc 保证第 1 页就含刚发出的评论（review P1-3）。
	listOpts := githubpkg.ListOptions{PerPage: 100}
	sort, direction := "created", "asc"
	if opts.Desc {
		direction = "desc"
	}
	list, _, err := g.client.Issues.ListComments(ctx, g.owner, g.name, int(number),
		&githubpkg.IssueListCommentsOptions{
			Since:       opts.Since,
			Sort:        &sort,
			Direction:   &direction,
			ListOptions: listOpts,
		})
	if err != nil {
		return nil, err
	}
	out := make([]publisher.Comment, 0, len(list))
	for _, c := range list {
		out = append(out, publisher.Comment{
			ID:        c.GetID(),
			URL:       c.GetHTMLURL(),
			Author:    c.GetUser().GetLogin(),
			Body:      c.GetBody(),
			CreatedAt: c.GetCreatedAt().Time,
		})
	}
	return out, nil
}

func (g *ghOps) CreateComment(ctx context.Context, number int64, body string) (int64, string, error) {
	c, _, err := g.client.Issues.CreateComment(ctx, g.owner, g.name, int(number),
		&githubpkg.IssueComment{Body: githubpkg.String(body)})
	if err != nil {
		return 0, "", err
	}
	return c.GetID(), c.GetHTMLURL(), nil
}
