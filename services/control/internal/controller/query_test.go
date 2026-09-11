package controller_test

// T026 查询 API 测试：能力检查 422、列表/详情形状、取消状态机。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/github"
)

// fakeProber 可导演能力缺失。
type fakeProber struct {
	getRepoErr   error
	listIssueErr error
}

func (f *fakeProber) GetRepo(ctx context.Context, installationID int64, owner, name string) (*github.RepoSummary, error) {
	if f.getRepoErr != nil {
		return nil, f.getRepoErr
	}
	return &github.RepoSummary{DefaultBranch: "main"}, nil
}

func (f *fakeProber) ListIssues(ctx context.Context, installationID int64, owner, name string, limit int) ([]int64, error) {
	if f.listIssueErr != nil {
		return nil, f.listIssueErr
	}
	return []int64{1}, nil
}

func newQueryRouter(t *testing.T, prober github.CapabilityProber) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, claim, contents, artifacts, _ := executeFixture(t)
	owner := "worker-exec"
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, owner, artifacts)
	if _, err := exec.Execute(t.Context(), claim); err != nil {
		t.Fatalf("执行: %v", err)
	}
	_ = st
	r := gin.New()
	q := controller.NewQueryHandler(st, prober)
	api := r.Group("/api")
	{
		api.GET("/cases", q.ListCases)
		api.GET("/cases/:caseID", q.GetCase)
		api.GET("/runs/:runID", q.GetRun)
		api.POST("/runs/:runID/cancel", q.CancelRun)
		api.POST("/repositories", q.OnboardRepository)
	}
	return r
}

func doReq(t *testing.T, r *gin.Engine, method, path, body string) (int, string) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestQueryAPI(t *testing.T) {
	t.Run("case 列表与详情含草稿/费用", func(t *testing.T) {
		r := newQueryRouter(t, &fakeProber{})
		code, body := doReq(t, r, http.MethodGet, "/api/cases", "")
		if code != http.StatusOK || !strings.Contains(body, `"issue_number"`) {
			t.Fatalf("列表: %d %s", code, body[:min(200, len(body))])
		}
		caseID := between(body, `"id":"`, `"`)
		code, body = doReq(t, r, http.MethodGet, "/api/cases/"+caseID, "")
		if code != http.StatusOK {
			t.Fatalf("详情: %d", code)
		}
		for _, want := range []string{`"runs"`, `"draft"`, `"conclusion":"ANSWER_READY"`, `"evidence"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("详情缺 %s: %s", want, body[:min(300, len(body))])
			}
		}
	})

	t.Run("run 详情含提交轨迹与费用", func(t *testing.T) {
		r := newQueryRouter(t, &fakeProber{})
		_, body := doReq(t, r, http.MethodGet, "/api/cases", "")
		// 从详情拿 run id
		caseID := between(body, `"id":"`, `"`)
		_, detail := doReq(t, r, http.MethodGet, "/api/cases/"+caseID, "")
		runID := between(detail, `"id":"run-`, `"`)
		runID = "run-" + runID
		code, body := doReq(t, r, http.MethodGet, "/api/runs/"+runID, "")
		if code != http.StatusOK {
			t.Fatalf("run 详情: %d", code)
		}
		for _, want := range []string{`"commits"`, `"model_calls"`, `"cost_cny"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("run 详情缺 %s", want)
			}
		}
	})

	t.Run("取消终态 run：409 回显状态", func(t *testing.T) {
		r := newQueryRouter(t, &fakeProber{})
		_, body := doReq(t, r, http.MethodGet, "/api/cases", "")
		caseID := between(body, `"id":"`, `"`)
		_, detail := doReq(t, r, http.MethodGet, "/api/cases/"+caseID, "")
		runID := "run-" + between(detail, `"id":"run-`, `"`)
		code, resp := doReq(t, r, http.MethodPost, "/api/runs/"+runID+"/cancel", "")
		if code != http.StatusConflict || !strings.Contains(resp, "COMPLETED") {
			t.Fatalf("终态取消应 409: %d %s", code, resp)
		}
	})

	t.Run("能力缺失：422 带缺失清单（AC01）", func(t *testing.T) {
		r := newQueryRouter(t, &fakeProber{listIssueErr: errors.New("403 forbidden")})
		code, body := doReq(t, r, http.MethodPost, "/api/repositories",
			`{"repo_numeric_id":42,"owner":"o","name":"n","installation_id":7}`)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("应 422: %d %s", code, body)
		}
		var parsed struct {
			Missing []string `json:"missing"`
		}
		if err := json.Unmarshal([]byte(body), &parsed); err != nil || len(parsed.Missing) == 0 {
			t.Fatalf("应带缺失清单: %s", body)
		}
	})

	t.Run("能力齐全：接入成功落 active", func(t *testing.T) {
		r := newQueryRouter(t, &fakeProber{})
		code, body := doReq(t, r, http.MethodPost, "/api/repositories",
			`{"repo_numeric_id":4200,"owner":"o","name":"fresh","installation_id":7}`)
		if code != http.StatusOK {
			t.Fatalf("应 200: %d %s", code, body)
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
