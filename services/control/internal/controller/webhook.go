package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	githubpkg "github.com/google/go-github/v90/github"

	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// WebhookHandler 处理 GitHub webhook：验签 → 登记 → （范围内）建 case+run。
// 性能预算：1 秒内持久化并响应（PRD §26.2）——所有重活（取文件、调模型）
// 都不属于这里，只创建 QUEUED 的 run 交由 runner 异步执行。
type WebhookHandler struct {
	store  *store.Store
	secret string
}

func NewWebhookHandler(st *store.Store, secret string) *WebhookHandler {
	return &WebhookHandler{store: st, secret: secret}
}

var errDuplicateDelivery = errors.New("重复投递")

// Handle 是 Gin 的入口。
// 响应约定：GitHub 只关心 2xx 与否（非 2xx 会重试投递），
// 202 = 已受理有后续，200 = 已知无需处理（含重复投递），401 = 验签拒绝。
func (h *WebhookHandler) Handle(c *gin.Context) {
	start := time.Now()

	// 原始字节先行：验签必须针对未经任何解码的 body（T009 的 ParseDelivery 契约）
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}

	delivery, err := github.ParseDelivery(h.secret, c.Request.Header, body)
	if err != nil {
		// 验签失败时 ParseDelivery 不产出 Delivery，元数据直接从 header 取——
		// 即使是恶意请求，事件头仍可用于留痕取证。
		h.reject(c,
			c.Request.Header.Get("X-GitHub-Event"),
			c.Request.Header.Get("X-GitHub-Delivery"),
			body, err)
		return
	}

	issuesEvt, ok := delivery.Event.(*githubpkg.IssuesEvent)
	if !ok {
		h.ignore(c, delivery, body, 0, "非 issues 事件")
		return
	}

	// 仓库范围检查：范围外照记不处理（spec FR-2）。
	// 只读查询放在事务外：失败与业务写入无关，事务里少一个变量。
	repo, err := h.store.GetRepositoryByNumericID(c.Request.Context(), issuesEvt.Repo.GetID())
	if errors.Is(err, store.ErrNoRows) {
		h.ignore(c, delivery, body, issuesEvt.Repo.GetID(), "范围外仓库")
		return
	}
	if err != nil {
		slog.Error("查询仓库失败", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
		return
	}

	// 事件过滤规则集中在 events.go（纯函数，含防循环与 reopened 语义）
	if rej := decideIssueEvent(issuesEvt); rej.ignore {
		h.ignore(c, delivery, body, issuesEvt.Repo.GetID(), rej.reason)
		return
	}

	result, err := h.createCaseAndRun(c.Request.Context(), delivery, body, repo, issuesEvt)
	if err != nil {
		if errors.Is(err, errDuplicateDelivery) {
			// 重复投递（AC45）：幂等返回 200，GitHub 无需重试
			slog.Info("重复投递已忽略", "delivery", delivery.DeliveryID)
			c.JSON(http.StatusOK, gin.H{"status": "deduplicated"})
			return
		}
		slog.Error("创建 case/run 失败", "err", err, "delivery", delivery.DeliveryID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
		return
	}

	slog.Info("webhook 已受理", "delivery", delivery.DeliveryID,
		"case", result.caseID, "run", result.runID, "elapsed", time.Since(start))
	c.JSON(http.StatusAccepted, gin.H{
		"status":   "accepted",
		"event_id": result.eventID,
		"case_id":  result.caseID,
		"run_id":   result.runID,
	})
}

type createResult struct{ eventID, caseID, runID string }

// createCaseAndRun 在一个事务里完成：事件登记 → 建 case（或复用）→ 建 run → 关联。
// 事务边界 = 业务边界：四步要么同生共死，要么全回滚——
// 绝不允许出现"有 run 无事件"或"事件标 processed 但没 run"的中间态。
func (h *WebhookHandler) createCaseAndRun(ctx context.Context, d *github.Delivery, body []byte, repo db.Repository, evt *githubpkg.IssuesEvent) (createResult, error) {
	eventID := ids.New("event")
	caseID := ids.New("case")
	runID := ids.New("run")

	// input_snapshot：触发时刻的固定快照（FR-3）。
	// 只取分析需要的字段并钉住内容——之后 Issue 被编辑也不影响本次执行。
	snapshot, err := json.Marshal(map[string]any{
		"issue": map[string]any{
			"number":     evt.Issue.GetNumber(),
			"id":         evt.Issue.GetID(),
			"title":      evt.Issue.GetTitle(),
			"body":       evt.Issue.GetBody(),
			"author":     evt.Issue.User.GetLogin(), // openapi 要求 issue.author
			"updated_at": evt.Issue.GetUpdatedAt().Format(time.RFC3339),
		},
		"repo": map[string]any{
			"numeric_id":     repo.RepoNumericID,
			"owner":          repo.Owner,
			"name":           repo.Name,
			"default_branch": repo.DefaultBranch,
		},
		"policy_version": repo.PolicyVersion,
	})
	if err != nil {
		return createResult{}, err
	}

	err = h.store.WithTx(ctx, func(q *db.Queries) error {
		// 注意用 = 而非 :=：闭包内短声明会遮蔽外层变量，
		// 返回给调用方的就是错 ID（尤其 UpsertCase 命中已有 case 时）。
		evID, err := insertEvent(ctx, q, d, body, repo.RepoNumericID)
		if err != nil {
			return err
		}
		eventID = evID

		cid, err := q.UpsertCase(ctx, db.UpsertCaseParams{
			ID:          caseID,
			RepoID:      repo.ID,
			IssueNumber: int64(evt.Issue.GetNumber()),
			IssueID:     evt.Issue.GetID(),
			Title:       evt.Issue.GetTitle(),
		})
		if err != nil {
			return err
		}
		caseID = cid

		if _, err := q.InsertRun(ctx, db.InsertRunParams{
			ID: runID, CaseID: caseID, TriggerEventID: eventID, InputSnapshot: snapshot,
		}); err != nil {
			return err
		}

		return q.LinkInboxEventToRun(ctx, db.LinkInboxEventToRunParams{
			ID: eventID, RunID: &runID,
		})
	})
	if err != nil {
		return createResult{}, err
	}
	return createResult{eventID: eventID, caseID: caseID, runID: runID}, nil
}

// insertEvent 把事件原件登记进 inbox_events（status=received）。
// delivery_id 唯一冲突翻译为 errDuplicateDelivery，由调用方决定响应语义（AC45）。
// 注意冲突的表现形式：ON CONFLICT DO NOTHING 不抛 23505，
// 而是 RETURNING 落空 → pgx ErrNoRows——「空结果即重复」。
func insertEvent(ctx context.Context, q *db.Queries, d *github.Delivery, body []byte, repoNumericID int64) (string, error) {
	eventID := ids.New("event")
	action := "unknown"
	if evt, ok := d.Event.(*githubpkg.IssuesEvent); ok {
		action = evt.GetAction()
	}
	_, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
		ID:             eventID,
		DeliveryID:     d.DeliveryID,
		EventType:      d.EventType,
		Action:         &action,
		RepoNumericID:  &repoNumericID,
		Payload:        body,
		SignatureValid: true,
	})
	return eventID, translateDuplicate(err)
}

// translateDuplicate 把「冲突被忽略」的两种表现统一成 errDuplicateDelivery：
// 主路径是 RETURNING 落空（ErrNoRows）；23505 分支是防御性兜底，
// 以防未来查询改成 ON CONFLICT DO UPDATE 或加约束时语义漂移。
func translateDuplicate(err error) error {
	switch {
	case errors.Is(err, store.ErrNoRows):
		return errDuplicateDelivery
	case store.IsUniqueViolation(err, "inbox_events_delivery_id_key"):
		return errDuplicateDelivery
	default:
		return err
	}
}

// ignore 登记事件（留痕取证）并标记 ignored，幂等返回 200——
// 范围外/不满足过滤条件的事件若返回非 2xx，GitHub 会反复重投。
// repoNumericID 为 0 表示事件里没有可解析的仓库信息。
func (h *WebhookHandler) ignore(c *gin.Context, d *github.Delivery, body []byte, repoNumericID int64, reason string) {
	err := h.store.WithTx(c.Request.Context(), func(q *db.Queries) error {
		eventID, err := insertEvent(c.Request.Context(), q, d, body, repoNumericID)
		if err != nil {
			return err
		}
		return q.MarkInboxEventStatus(c.Request.Context(), db.MarkInboxEventStatusParams{
			ID: eventID, ProcessStatus: "ignored",
		})
	})
	if errors.Is(err, errDuplicateDelivery) {
		c.JSON(http.StatusOK, gin.H{"status": "deduplicated"})
		return
	}
	if err != nil {
		// 留痕失败不改变拒绝语义：照常 200，别让 GitHub 重投一件我们不想收的事
		slog.Error("登记 ignored 事件失败", "err", err, "delivery", d.DeliveryID)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": reason})
}

// reject 记录验签失败（signature_valid=false）并 401 拒绝。
// 留痕是尽力而为：攻击者的请求不该有能力让我们的库或响应出问题。
func (h *WebhookHandler) reject(c *gin.Context, eventType, deliveryID string, body []byte, reason error) {
	slog.Warn("webhook 验签拒绝", "err", reason, "remote", c.ClientIP())
	if eventType != "" && deliveryID != "" {
		err := h.store.WithTx(c.Request.Context(), func(q *db.Queries) error {
			eventID, err := insertEventUnsigned(c.Request.Context(), q, eventType, deliveryID, body)
			if err != nil {
				return err
			}
			return q.MarkInboxEventStatus(c.Request.Context(), db.MarkInboxEventStatusParams{
				ID: eventID, ProcessStatus: "ignored",
			})
		})
		if err != nil {
			slog.Error("记录被拒事件失败", "err", err)
		}
	}
	c.JSON(http.StatusUnauthorized, gin.H{"error": reason.Error()})
}

// insertEventUnsigned 与 insertEvent 的差别只在 signature_valid=false——
// 签名不可信时不去解析载荷类型（payload 可能是伪造的）。
func insertEventUnsigned(ctx context.Context, q *db.Queries, eventType, deliveryID string, body []byte) (string, error) {
	eventID := ids.New("event")
	_, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
		ID:             eventID,
		DeliveryID:     deliveryID,
		EventType:      eventType,
		Payload:        body,
		SignatureValid: false,
	})
	return eventID, translateDuplicate(err)
}
