package controller_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

const openedPayload = `{"action":"opened","issue":{"id":5001,"number":7,"title":"标题","body":"正文"},"repository":{"id":42,"name":"n","owner":{"login":"o"}}}`
const openedPayload2 = `{"action":"opened","issue":{"id":5002,"number":8,"title":"另一个","body":"正文"},"repository":{"id":42,"name":"n","owner":{"login":"o"}}}`
const editedPayload = `{"action":"edited","issue":{"id":5001,"number":7,"title":"标题","body":"正文2"},"repository":{"id":42,"name":"n","owner":{"login":"o"}}}`
const foreignPayload = `{"action":"opened","issue":{"id":9001,"number":1,"title":"x","body":"y"},"repository":{"id":999,"name":"far","owner":{"login":"other"}}}`

func signedRequest(t *testing.T, secret, event, deliveryID, payload string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sig)
	return req
}

// newWebhookRouter 迁移清场 + 接好 handler，返回路由与库池（断言用）。
func newWebhookRouter(t *testing.T, secret string) (*gin.Engine, *pgxpool.Pool) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	// 范围内仓库：repo_numeric_id=42（openedPayload 里的 repository.id）
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, policy_version)
		VALUES ('repo-in', 42, 'o', 'n', 'main', 7, 'v1')`); err != nil {
		t.Fatalf("seed repository: %v", err)
	}

	r := gin.New()
	r.POST("/webhook", controller.NewWebhookHandler(st, secret).Handle)
	return r, pool
}

func do(t *testing.T, r *gin.Engine, req *http.Request) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Body)
	return w.Code, string(body)
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("统计查询失败: %v", err)
	}
	return n
}

func TestWebhook(t *testing.T) {
	const secret = "hook-secret"

	t.Run("opened 受理：202 + 事件/case/run 全部落库", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		code, body := do(t, r, signedRequest(t, secret, "issues", "d-1", openedPayload))
		if code != http.StatusAccepted {
			t.Fatalf("应 202，得到 %d %s", code, body)
		}
		if got := count(t, pool, `SELECT count(*) FROM inbox_events WHERE process_status='processed'`); got != 1 {
			t.Fatalf("应有 1 条 processed 事件，得到 %d", got)
		}
		if got := count(t, pool, `SELECT count(*) FROM cases WHERE issue_number=7`); got != 1 {
			t.Fatalf("应有 1 条 case，得到 %d", got)
		}
		if got := count(t, pool, `SELECT count(*) FROM runs WHERE status='QUEUED'`); got != 1 {
			t.Fatalf("应有 1 条 QUEUED run，得到 %d", got)
		}
		// 输入快照必须钉住触发时刻的内容（FR-3）
		if got := count(t, pool, `SELECT count(*) FROM runs WHERE input_snapshot::text LIKE '%"updated_at"%' AND input_snapshot::text LIKE '%"policy_version"%'`); got != 1 {
			t.Fatal("input_snapshot 缺少 issue 版本或策略版本字段")
		}
	})

	t.Run("同 delivery 重投：200 去重，不重复建 run（AC45）", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		if code, _ := do(t, r, signedRequest(t, secret, "issues", "d-2", openedPayload)); code != http.StatusAccepted {
			t.Fatalf("首次应 202")
		}
		code, _ := do(t, r, signedRequest(t, secret, "issues", "d-2", openedPayload))
		if code != http.StatusOK {
			t.Fatalf("重投应 200，得到 %d", code)
		}
		if got := count(t, pool, `SELECT count(*) FROM runs`); got != 1 {
			t.Fatalf("重投后仍应只有 1 条 run，得到 %d", got)
		}
	})

	t.Run("edited 事件：200 ignored，不建 run", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		code, _ := do(t, r, signedRequest(t, secret, "issues", "d-3", editedPayload))
		if code != http.StatusOK {
			t.Fatalf("应 200，得到 %d", code)
		}
		if got := count(t, pool, `SELECT count(*) FROM inbox_events WHERE process_status='ignored'`); got != 1 {
			t.Fatalf("事件应记为 ignored，得到 %d", got)
		}
		if got := count(t, pool, `SELECT count(*) FROM runs`); got != 0 {
			t.Fatalf("edited 不应建 run，得到 %d", got)
		}
	})

	t.Run("范围外仓库：200 ignored，事件留痕", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		code, _ := do(t, r, signedRequest(t, secret, "issues", "d-4", foreignPayload))
		if code != http.StatusOK {
			t.Fatalf("应 200，得到 %d", code)
		}
		if got := count(t, pool, `SELECT count(*) FROM inbox_events WHERE process_status='ignored' AND repo_numeric_id=999`); got != 1 {
			t.Fatalf("范围外事件应留痕(ignored, repo=999)，得到 %d", got)
		}
	})

	t.Run("验签失败：401 拒绝并留痕 signature_valid=false", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		code, _ := do(t, r, signedRequest(t, "wrong-secret", "issues", "d-5", openedPayload))
		if code != http.StatusUnauthorized {
			t.Fatalf("应 401，得到 %d", code)
		}
		if got := count(t, pool, `SELECT count(*) FROM inbox_events WHERE signature_valid=false`); got != 1 {
			t.Fatalf("被拒事件应留痕，得到 %d", got)
		}
		if got := count(t, pool, `SELECT count(*) FROM runs`); got != 0 {
			t.Fatalf("被拒事件不应建 run，得到 %d", got)
		}
	})

	t.Run("不同 issue 同仓：各建各的 case 与 run", func(t *testing.T) {
		r, pool := newWebhookRouter(t, secret)
		do(t, r, signedRequest(t, secret, "issues", "d-6a", openedPayload))
		do(t, r, signedRequest(t, secret, "issues", "d-6b", openedPayload2))
		if got := count(t, pool, `SELECT count(*) FROM cases`); got != 2 {
			t.Fatalf("两个 issue 应有 2 条 case，得到 %d", got)
		}
		if got := count(t, pool, `SELECT count(*) FROM runs`); got != 2 {
			t.Fatalf("应有 2 条 run，得到 %d", got)
		}
	})
}
