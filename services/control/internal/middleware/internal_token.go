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

// TokenAuth 是按请求头做共享令牌鉴权的通用中间件。
// 匹配用 constant-time 比较，避免计时侧信道逐字节猜测令牌；
// 未携带或错误统一 401，不区分两种失败（不向探测者泄露信息）。
// 适用两类场景：服务间调用（X-DevFlow-Internal-Token）与操作者 API（X-Operator-Token）。
func TokenAuth(header, token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader(header)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "缺少或错误的令牌",
			})
			return
		}
		c.Next()
	}
}

// InternalTokenAuth 保护仅限服务间调用的路由（如 /internal/*）。
func InternalTokenAuth(token string) gin.HandlerFunc {
	return TokenAuth(InternalTokenHeader, token)
}

// OperatorTokenHeader 是操作者 API 的身份头（M1 单操作者共享令牌，宪法 II）。
const OperatorTokenHeader = "X-Operator-Token"

// OperatorAuth 保护操作者 API：服务端认证身份，不信任请求声明（AC32）。
func OperatorAuth(token string) gin.HandlerFunc {
	return TokenAuth(OperatorTokenHeader, token)
}
