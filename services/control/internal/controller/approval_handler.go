package controller

// approval_handler.go（T021）：审批域的 HTTP 端点。
// 职责只有两件事：把服务错误翻译成 HTTP 语义，把响应收敛成稳定形状；
// 业务规则全部在 ApprovalService（可脱离 HTTP 测试）。

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// ApprovalHandler 是审批域的 Gin 处理器集合。
type ApprovalHandler struct {
	svc *ApprovalService
}

func NewApprovalHandler(svc *ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc}
}

// Create 在 case 上生成审批包（操作者已通过 OperatorAuth）。
func (h *ApprovalHandler) Create(c *gin.Context) {
	b, err := h.svc.CreateBundle(c.Request.Context(), c.Param("caseID"))
	switch {
	case errors.Is(err, ErrNoDraft):
		c.JSON(http.StatusNotFound, gin.H{"error": "该 case 没有可审批的草稿"})
		return
	case errors.Is(err, ErrNotApprovable):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		if errors.Is(err, store.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "case 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
		return
	}
	// 200/201 都合理；这里统一 200——重复创建幂等回显同包，调用方无需区分
	c.JSON(http.StatusOK, bundleJSON(b))
}

// Approve 批准审批包。AC35：重复批准回显原结果（200）；
// AC33：过期/内容漂移 → 409 且包翻 EXPIRED。
func (h *ApprovalHandler) Approve(c *gin.Context) {
	// 操作者身份由服务端令牌认证（AC32）；M1 单操作者，operator 标记固定
	b, err := h.svc.Approve(c.Request.Context(), c.Param("bundleID"), "operator")
	switch {
	case err == nil:
		c.JSON(http.StatusOK, bundleJSON(b))
	case errors.Is(err, ErrBundleExpired), errors.Is(err, ErrBundleNotState):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "bundle": bundleJSON(b)})
	case errors.Is(err, store.ErrNoRows):
		c.JSON(http.StatusNotFound, gin.H{"error": "审批包不存在"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
	}
}

// Reject 拒绝审批包（仅 PENDING 可拒）。
func (h *ApprovalHandler) Reject(c *gin.Context) {
	b, err := h.svc.Reject(c.Request.Context(), c.Param("bundleID"))
	switch {
	case err == nil:
		c.JSON(http.StatusOK, bundleJSON(b))
	case errors.Is(err, ErrBundleNotState):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "bundle": bundleJSON(b)})
	case errors.Is(err, store.ErrNoRows):
		c.JSON(http.StatusNotFound, gin.H{"error": "审批包不存在"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
	}
}

// bundleJSON 是审批包的对外形状（显式挑字段：idempotency_key 等内部列不外泄）。
func bundleJSON(b db.ApprovalBundle) gin.H {
	var approvedAt any
	if b.ApprovedAt.Valid {
		approvedAt = b.ApprovedAt.Time
	}
	return gin.H{
		"id":             b.ID,
		"case_id":        b.CaseID,
		"draft_id":       b.DraftID,
		"status":         b.Status,
		"action_type":    b.ActionType,
		"target":         json.RawMessage(b.Target),
		"content_digest": b.ContentDigest,
		"expires_at":     b.ExpiresAt.Time,
		"approved_by":    b.ApprovedBy,
		"approved_at":    approvedAt,
	}
}
