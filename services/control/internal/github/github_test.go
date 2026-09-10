package github

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"testing"
)

// sign 用与 GitHub 相同的算法（HMAC-SHA256）为 payload 计算签名头值。
func sign(t *testing.T, secret string, payload []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func deliveryHeaders(event, deliveryID, signature string) http.Header {
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", deliveryID)
	h.Set("X-Hub-Signature-256", signature)
	return h
}

const issuesPayload = `{"action":"opened","issue":{"number":1},"repository":{"id":42}}`

func TestParseDelivery(t *testing.T) {
	const secret = "webhook-secret"

	t.Run("合法签名解析成功", func(t *testing.T) {
		d, err := ParseDelivery(secret,
			deliveryHeaders("issues", "uuid-1", sign(t, secret, []byte(issuesPayload))),
			[]byte(issuesPayload))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if d.EventType != "issues" || d.DeliveryID != "uuid-1" {
			t.Fatalf("元数据不符: %+v", d)
		}
		if d.Event == nil {
			t.Fatal("类型化事件不应为 nil")
		}
		if string(d.Payload) != issuesPayload {
			t.Fatal("Payload 应保留原始字节")
		}
	})

	t.Run("签名不符返回 ErrInvalidSignature", func(t *testing.T) {
		// 用 errors.Is 断言哨兵值本身：错误消息可以改，包边界契约不能破
		_, err := ParseDelivery(secret,
			deliveryHeaders("issues", "uuid-2", sign(t, "另外的密钥", []byte(issuesPayload))),
			[]byte(issuesPayload))
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("应 errors.Is 命中 ErrInvalidSignature，得到 %v", err)
		}
	})

	t.Run("空 secret 直接拒绝", func(t *testing.T) {
		_, err := ParseDelivery("",
			deliveryHeaders("issues", "uuid-4", sign(t, secret, []byte(issuesPayload))),
			[]byte(issuesPayload))
		if err == nil || errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("空 secret 应报配置错误而非签名错误: %v", err)
		}
	})

	t.Run("完全缺失签名头同样拒绝", func(t *testing.T) {
		h := deliveryHeaders("issues", "uuid-5", "")
		h.Del("X-Hub-Signature-256")
		_, err := ParseDelivery(secret, h, []byte(issuesPayload))
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("缺签名头应命中 ErrInvalidSignature，得到 %v", err)
		}
	})

	t.Run("签名合法但事件类型未知→解析错误而非签名错误", func(t *testing.T) {
		_, err := ParseDelivery(secret,
			deliveryHeaders("mystery_event", "uuid-6", sign(t, secret, []byte(issuesPayload))),
			[]byte(issuesPayload))
		if err == nil || errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("未知事件类型应走解析错误路径: %v", err)
		}
	})

	t.Run("伪造签名（非法 hex）不 panic", func(t *testing.T) {
		_, err := ParseDelivery(secret,
			deliveryHeaders("issues", "uuid-3", "sha256=zz-not-hex"),
			[]byte(issuesPayload))
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("非法 hex 应校验失败而非 panic: %v", err)
		}
	})

	t.Run("签名合法但缺元数据头直接拒绝", func(t *testing.T) {
		// 验签顺序在前：签名必须合法才能测到「缺元数据头」分支
		h := http.Header{}
		h.Set("X-Hub-Signature-256", sign(t, secret, []byte(issuesPayload)))
		if _, err := ParseDelivery(secret, h, []byte(issuesPayload)); err == nil {
			t.Fatal("缺元数据头应报错")
		}
	})
}

func TestNewClientFactory(t *testing.T) {
	t.Run("合法 RSA 私钥可构造且 Client 带缓存", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("生成密钥: %v", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})

		f, err := NewClientFactory(123, pemBytes)
		if err != nil {
			t.Fatalf("构造工厂: %v", err)
		}
		c1, err := f.Client(456)
		if err != nil {
			t.Fatalf("获取 client: %v", err)
		}
		c2, err := f.Client(456)
		if err != nil {
			t.Fatalf("再次获取 client: %v", err)
		}
		if c1 != c2 {
			t.Fatal("同一 installation 应返回缓存的同一 client 实例")
		}
		c3, err := f.Client(789)
		if err != nil {
			t.Fatalf("获取另一 installation 的 client: %v", err)
		}
		if c1 == c3 {
			t.Fatal("不同 installation 不应共享 client（token 身份不同）")
		}
	})

	t.Run("垃圾私钥报错", func(t *testing.T) {
		if _, err := NewClientFactory(123, []byte("not a pem")); err == nil {
			t.Fatal("非法 PEM 应报错")
		}
	})
}
