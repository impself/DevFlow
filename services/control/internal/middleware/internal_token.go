// Package middleware 放 Gin 中间件。每个中间件只做一件事，
// 由路由装配处按需组合（与 gin.Default() 的"全家桶"相反，依赖可见）。
package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

// InternalTokenHeader 是 Go ↔ Python 服务间调用的身份头。
// 常量集中定义：两边（Go 校验、Go 出站调用携带）引同一处，防止字符串漂移。
const InternalTokenHeader = "X-DevFlow-Internal-Token"

// InternalTokenAuth 校验内部令牌，用于保护仅限服务间调用的路由（如 /internal/*）。
// 匹配用 constant-time 比较，避免计时侧信道逐字节猜测令牌。
// 未携带或错误统一 401，不区分两种失败（不向探测者泄露信息）。
func InternalTokenAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader(InternalTokenHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "缺少或错误的内部令牌",
			})
			return
		}
		c.Next()
	}
}
