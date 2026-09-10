package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestRouter(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(InternalTokenAuth(token))
	r.GET("/internal/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func TestInternalTokenAuth(t *testing.T) {
	const token = "secret-token-123"
	r := newTestRouter(token)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"无令牌", "", http.StatusUnauthorized},
		{"错误令牌", "wrong", http.StatusUnauthorized},
		{"正确令牌", token, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/internal/ping", nil)
			if tc.header != "" {
				req.Header.Set(InternalTokenHeader, tc.header)
			}
			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, 期望 %d", w.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("401 响应应为 JSON: %v", err)
				}
			}
		})
	}
}
