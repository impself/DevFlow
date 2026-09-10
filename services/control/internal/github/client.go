// Package github 收口对 GitHub 的一切访问：App 身份（installation token）、
// 客户端工厂与 webhook 验签解析。凭据只存在于本包（宪法 III），
// 其余代码只面对 *github.Client 与解析后的事件对象。
package github

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	githubpkg "github.com/google/go-github/v90/github"
)

// ClientFactory 按 installation ID 产出并缓存 *github.Client。
//
// GitHub App 的调用身份不是 App 本身，而是"装到某个组织/仓库上的 installation"：
// token 按 installation 发放（约 1 小时有效，ghinstallation 自动用 App JWT 换新并缓存）。
// 仓库可以改名换 owner，installation_id 不变——所以缓存键用 installation ID，
// 与 repositories 表的锚点一致。
type ClientFactory struct {
	appsTransport *ghinstallation.AppsTransport

	mu    sync.Mutex
	cache map[int64]*githubpkg.Client
}

// NewClientFactory 用 App ID 与 PEM 私钥内容构造工厂。
// 私钥内容由调用方从 config 指定的路径读取，本包不碰文件系统与环境变量。
func NewClientFactory(appID int64, privateKeyPEM []byte) (*ClientFactory, error) {
	apps, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("解析 App 私钥: %w", err)
	}
	return &ClientFactory{
		appsTransport: apps,
		cache:         make(map[int64]*githubpkg.Client),
	}, nil
}

// Client 返回能以指定 installation 身份调用 API 的客户端（带缓存）。
// 首次调用只构造 transport 不发起网络请求；token 在第一次真实 API 调用时才换取。
func (f *ClientFactory) Client(installationID int64) (*githubpkg.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if client, ok := f.cache[installationID]; ok {
		return client, nil
	}
	tr := ghinstallation.NewFromAppsTransport(f.appsTransport, installationID)
	// Timeout 必须显式给：http.Client 零值无超时，一次悬挂响应就能卡死
	// 并发上限为 1 的 worker 领取循环（租约 30s，超时上限必须小于它）。
	// go-github v90 起改为函数式选项且返回 error（旧版 NewClient(httpClient) 已废弃）。
	client, err := githubpkg.NewClient(githubpkg.WithHTTPClient(&http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}))
	if err != nil {
		return nil, fmt.Errorf("构造 GitHub 客户端: %w", err)
	}
	f.cache[installationID] = client
	return client, nil
}
