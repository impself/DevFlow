package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	githubpkg "github.com/google/go-github/v90/github"
)

// ErrInvalidSignature：验签失败。controller 据此决定落库标记 signature_valid=false
// 并拒绝处理——错误语义在包边界定义，上层不需要懂 HMAC 细节。
var ErrInvalidSignature = errors.New("webhook 签名校验失败")

// Delivery 携带一次 webhook 投递的元数据与解析结果。
// Raw 保留原始字节（inbox_events.payload 落库要求原文，AC 取证可重放）。
type Delivery struct {
	EventType  string // X-GitHub-Event 头，如 "issues"
	DeliveryID string // X-GitHub-Delivery 头，投递去重锚点（AC45）
	Payload    []byte // 原始 JSON
	Event      any    // ParseWebHook 的类型化结果（如 *githubpkg.IssuesEvent）
}

// ParseDelivery = 验签 + 解析的封装（顺序不可换：先验签，再解析）。
//
// 为什么不用 go-github 的 ValidatePayload（直接吃 *http.Request）？
// webhook handler 在 Gin 里还需要精确控制"读 body → 失败标记 → 落库"的流程，
// 吃 request 的封装会把 body 一次性读走、错误分类也粗。这里收窄为纯函数：
// 输入 header + body，输出 Delivery 或错误——可单测、无隐藏 IO。
func ParseDelivery(secret string, header http.Header, body []byte) (*Delivery, error) {
	// 空 secret 意味着误配置：任何攻击者都能自行计算合法 HMAC 绕过验签。
	// 在第一次请求就报错，而不是静默放行（fail-fast，同 config.Load 的哲学）。
	if secret == "" {
		return nil, errors.New("webhook secret 为空，拒绝处理（检查 GITHUB_WEBHOOK_SECRET 配置）")
	}

	// 先验签再查元数据头：未签名的探测请求与格式异常的请求走同一条失败路径，
	// 不向外部泄露"哪一步没过"的区分信息。
	if !validateSignature(header.Get("X-Hub-Signature-256"), body, secret) {
		return nil, ErrInvalidSignature
	}

	eventType := header.Get("X-GitHub-Event")
	deliveryID := header.Get("X-GitHub-Delivery")
	if eventType == "" || deliveryID == "" {
		return nil, errors.New("缺少 X-GitHub-Event 或 X-GitHub-Delivery 头")
	}

	event, err := githubpkg.ParseWebHook(eventType, body)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 载荷: %w", eventType, err)
	}
	return &Delivery{
		EventType:  eventType,
		DeliveryID: deliveryID,
		Payload:    bytes.Clone(body),
		Event:      event,
	}, nil
}

// validateSignature 校验 GitHub 的 HMAC-SHA256 签名（X-Hub-Signature-256: sha256=<hex>）。
// hmac.Equal 是 constant-time 比较——理由同 T005 的内部令牌（防计时侧信道）。
func validateSignature(signature string, payload []byte, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := make([]byte, hex.DecodedLen(len(signature)-len(prefix)))
	if _, err := hex.Decode(expected, []byte(signature[len(prefix):])); err != nil {
		return false
	}
	return hmac.Equal(expected, mac.Sum(nil))
}
