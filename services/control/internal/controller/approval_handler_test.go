package controller_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/middleware"
	"github.com/impself/DevFlow/services/control/internal/store"
)

// newApprovalRouter 走完「执行 → 草稿」后装配审批路由（与 api main 同构）。
func newApprovalRouter(t *testing.T) (*gin.Engine, *store.Store, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	st, claim, contents, artifacts, _ := executeFixture(t)
	owner := "worker-exec"
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, owner, artifacts)
	if _, err := exec.Execute(t.Context(), claim); err != nil {
		t.Fatalf("执行: %v", err)
	}

	r := gin.New()
	op := r.Group("/api", middleware.OperatorAuth("op-token"))
	{
		op.POST("/cases/:caseID/approval-bundle", controller.NewApprovalHandler(controller.NewApprovalService(st)).Create)
		op.POST("/bundles/:bundleID/approve", controller.NewApprovalHandler(controller.NewApprovalService(st)).Approve)
		op.POST("/bundles/:bundleID/reject", controller.NewApprovalHandler(controller.NewApprovalService(st)).Reject)
	}
	return r, st, claim.CaseID
}

func doJSON(t *testing.T, r *gin.Engine, method, path, token string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Operator-Token", token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestApprovalEndpoints(t *testing.T) {
	r, st, caseID := newApprovalRouter(t)

	t.Run("无令牌 401（AC32：服务端认证，不信任声明）", func(t *testing.T) {
		code, _ := doJSON(t, r, http.MethodPost, "/api/cases/"+caseID+"/approval-bundle", "")
		if code != http.StatusUnauthorized {
			t.Fatalf("应 401，得到 %d", code)
		}
	})

	// 创建审批包，拿 bundle id
	code, body := doJSON(t, r, http.MethodPost, "/api/cases/"+caseID+"/approval-bundle", "op-token")
	if code != http.StatusOK {
		t.Fatalf("创建审批包应 200: %d %s", code, body)
	}
	bundleID := between(body, `"id":"`, `"`)
	if bundleID == "" {
		t.Fatalf("响应应含 bundle id: %s", body)
	}

	t.Run("批准成功 200：APPROVED + 操作者落库", func(t *testing.T) {
		code, body := doJSON(t, r, http.MethodPost, "/api/bundles/"+bundleID+"/approve", "op-token")
		if code != http.StatusOK || !strings.Contains(body, `"status":"APPROVED"`) {
			t.Fatalf("应 200 APPROVED: %d %s", code, body)
		}
		var by *string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT approved_by FROM approval_bundles WHERE id=$1`, bundleID).Scan(&by); err != nil || by == nil {
			t.Fatalf("approved_by 应落库: %v", err)
		}
	})

	t.Run("重复批准回显原结果 200（AC35）", func(t *testing.T) {
		code, body := doJSON(t, r, http.MethodPost, "/api/bundles/"+bundleID+"/approve", "op-token")
		if code != http.StatusOK || !strings.Contains(body, `"status":"APPROVED"`) {
			t.Fatalf("重复批准应 200 回显: %d %s", code, body)
		}
	})

	t.Run("拒绝已批准的包 409（终态不可改写）", func(t *testing.T) {
		code, _ := doJSON(t, r, http.MethodPost, "/api/bundles/"+bundleID+"/reject", "op-token")
		if code != http.StatusConflict {
			t.Fatalf("应 409，得到 %d", code)
		}
	})
}

// between 取 body 中 first 与 second 之间的子串（轻量 JSON 字段提取，仅测试用）。
func between(s, first, second string) string {
	i := strings.Index(s, first)
	if i < 0 {
		return ""
	}
	s = s[i+len(first):]
	j := strings.Index(s, second)
	if j < 0 {
		return ""
	}
	return s[:j]
}
