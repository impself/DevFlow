package github

// 仓库内容预取：执行前把「固定 head SHA 上的文件」拉下来注入分析输入（FR-3）。
// 证据只允许引用这些固定内容——不抓任意外链（research.md §1 凭据教训）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	githubpkg "github.com/google/go-github/v90/github"
)

// FileContent 是预取到的单个文件（ref 固定后的内容快照）。
type FileContent struct {
	Path    string
	SHA     string // blob SHA，证据引用的锚点（AC03）
	Size    int
	Content []byte // 大于 Contents API 单文件上限（约 1MB）时为 nil，调用方按超限跳过
}

// RepoContentProvider 是内容预取的抽象：测试用假实现，生产用 GitHub。
// installationID 显式入参：同一接口服务多仓库，身份由数据（repositories 表）决定。
type RepoContentProvider interface {
	// HeadSHA 返回 ref（分支名）当前指向的 commit SHA——本次执行的「固定版本点」。
	HeadSHA(ctx context.Context, installationID int64, owner, name, ref string) (string, error)
	// FileAt 返回 ref 上的单个文件；文件不存在返回 (nil, nil)。
	FileAt(ctx context.Context, installationID int64, owner, name, ref, path string) (*FileContent, error)
}

// FactoryContents 用 ClientFactory 按 installation 解析客户端实现预取。
type FactoryContents struct {
	Factory *ClientFactory
}

func NewContents(f *ClientFactory) *FactoryContents { return &FactoryContents{Factory: f} }

func (g *FactoryContents) HeadSHA(ctx context.Context, installationID int64, owner, name, ref string) (string, error) {
	client, err := g.Factory.Client(installationID)
	if err != nil {
		return "", err
	}
	branch, _, err := client.Repositories.GetBranch(ctx, owner, name, ref, 1)
	if err != nil {
		return "", fmt.Errorf("获取 %s/%s@%s 分支信息: %w", owner, name, ref, err)
	}
	return branch.GetCommit().GetSHA(), nil
}

func (g *FactoryContents) FileAt(ctx context.Context, installationID int64, owner, name, ref, path string) (*FileContent, error) {
	client, err := g.Factory.Client(installationID)
	if err != nil {
		return nil, err
	}
	fc, _, _, err := client.Repositories.GetContents(ctx, owner, name, path,
		&githubpkg.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		var ghErr *githubpkg.ErrorResponse
		if errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound {
			return nil, nil // 文件不存在是正常形态（如仓库没有 README），不是错误
		}
		return nil, fmt.Errorf("获取 %s/%s@%s:%s: %w", owner, name, ref, path, err)
	}
	if fc == nil {
		return nil, nil // 路径是目录，按不存在处理
	}
	out := &FileContent{Path: path, SHA: fc.GetSHA(), Size: fc.GetSize()}
	if fc.Content != nil {
		// GetContent() 负责 base64 解码；Content 为 nil 表示文件超过 API 内联上限
		data, err := fc.GetContent()
		if err != nil {
			return nil, fmt.Errorf("解码 %s 内容: %w", path, err)
		}
		out.Content = []byte(data)
	}
	return out, nil
}
