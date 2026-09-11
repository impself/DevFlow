// Package runtimeclient 是 Go → Python 智能层的出站客户端。
// 请求/响应结构以 contracts/ 为唯一真源：请求镜像 openapi.yaml 的
// /internal/v1/issue-analysis 定义；响应的 schema 校验在 contract 包做。
package runtimeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/impself/DevFlow/services/control/internal/middleware"
)

// AnalysisRequest 镜像 openapi.yaml 请求体（run_id, issue, repo, files）。
type AnalysisRequest struct {
	RunID string     `json:"run_id"`
	Issue IssueInput `json:"issue"`
	Repo  RepoInput  `json:"repo"`
	Files []File     `json:"files,omitempty"`
}

type IssueInput struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Author string `json:"author"`
}

type RepoInput struct {
	NumericID int64  `json:"numeric_id"`
	FullName  string `json:"full_name"` // owner/name
	HeadSHA   string `json:"head_sha"`
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Client 是同步 HTTP 客户端。错误分层：
//   - ErrUnavailable：智能层明确返回 503（过载/降级）——调用方应重试而非失败；
//   - 其他错误：网络/协议问题，同样可重试但需要区分日志。
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// New 构造客户端。timeout 是单次分析的硬上限——智能层内部有自己的
// 模型调用预算，这里兜底防悬挂（run 总预算 900s 在执行器层另算）。
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		hc:      &http.Client{Timeout: timeout},
	}
}

// ErrUnavailable 表示智能层过载（503）。
var ErrUnavailable = fmt.Errorf("智能层过载")

// Analyze 调用 /internal/v1/issue-analysis，返回原始响应体。
// schema 复验不在本层——本层只负责传输与状态码语义，校验归 contract 包。
func (c *Client) Analyze(ctx context.Context, req AnalysisRequest) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化分析请求: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/internal/v1/issue-analysis", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(middleware.InternalTokenHeader, c.token)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用智能层: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 响应体 8MB 上限防失控
	if err != nil {
		return nil, fmt.Errorf("读取智能层响应: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, truncate(string(raw), 200))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("智能层返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
