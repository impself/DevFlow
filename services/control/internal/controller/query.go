package controller

// query.go（T026）：操作者工作台的查询与接入 API。
// 全部只读 + 一个最小取消状态机；鉴权在路由层（OperatorAuth）。
// 返回形状按 US3 前端需要组装（case 带 run 概要、run 带提交轨迹与费用）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// QueryHandler 是工作台 API 的处理器集合。
type QueryHandler struct {
	st     *store.Store
	prober github.CapabilityProber // 能力实测（GetRepo/ListIssues）
}

// NewQueryHandler 构造。resolver 可为 nil（无 GitHub 凭据的环境跳过能力实测）。
func NewQueryHandler(st *store.Store, prober github.CapabilityProber) *QueryHandler {
	return &QueryHandler{st: st, prober: prober}
}

// ---- 仓库接入（FR-1，AC01：能力缺失 422 返回清单）----

// requiredCapabilities 是 M1 分析链需要的仓库能力（PRD 最小权限清单）。
var requiredCapabilities = []string{"metadata_read", "contents_read", "issues_read", "issues_write"}

// OnboardRepository 实测能力后接入仓库。
// 能力检查失败 → 422 + 缺失清单（不落 active，spec FR-1/AC01）。
func (h *QueryHandler) OnboardRepository(c *gin.Context) {
	var req struct {
		RepoNumericID  int64  `json:"repo_numeric_id" binding:"required"`
		Owner          string `json:"owner" binding:"required"`
		Name           string `json:"name" binding:"required"`
		InstallationID int64  `json:"installation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if h.prober == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitHub 客户端未配置，无法实测能力"})
		return
	}
	caps, missing := h.probeCapabilities(c.Request.Context(), req.InstallationID, req.Owner, req.Name)
	if len(missing) > 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":    "仓库能力检查未通过",
			"missing":  missing,
			"required": requiredCapabilities,
		})
		return
	}

	repo, err := h.st.UpsertRepository(c.Request.Context(), db.UpsertRepositoryParams{
		ID:             ids.New("repo"),
		RepoNumericID:  req.RepoNumericID,
		Owner:          req.Owner,
		Name:           req.Name,
		DefaultBranch:  caps.defaultBranch,
		InstallationID: req.InstallationID,
		Capabilities:   caps.jsonb(),
		PolicyVersion:  "v1",
	})
	if err != nil {
		h.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"repository": repoJSON(repo)})
}

// probeCapabilities 实测仓库能力（读 metadata/默认分支/Issue 读取）。
// M1 简化：一次 Get 仓库成功 = metadata_read + contents_read + default_branch；
// Issues 读写用 list 一次 issue 旁证（写能力到 Checkpoint 场景 B 才真正验证）。
func (h *QueryHandler) probeCapabilities(ctx context.Context, installationID int64, owner, name string) (*probeResult, []string) {
	have := map[string]bool{}
	res := &probeResult{}

	if ghRepo, err := h.prober.GetRepo(ctx, installationID, owner, name); err == nil {
		have["metadata_read"] = true
		have["contents_read"] = true // Contents 读随仓库访问授权
		res.defaultBranch = ghRepo.DefaultBranch
	}
	if _, err := h.prober.ListIssues(ctx, installationID, owner, name, 1); err == nil {
		have["issues_read"] = true
		// issues_write 无法无损实测（写一条再删会留痕）——M1 以 App 权限配置为准，
		// 真正的验证发生在 Checkpoint 场景 B 的真实发布
		have["issues_write"] = true
	}

	var missing []string
	for _, want := range requiredCapabilities {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	return res, missing
}

type probeResult struct{ defaultBranch string }

func (p *probeResult) jsonb() []byte {
	b, _ := json.Marshal(map[string]bool{
		"metadata_read": true, "contents_read": true,
		"issues_read": true, "issues_write": true,
	})
	return b
}

// ---- 查询端点 ----

func (h *QueryHandler) ListRepositories(c *gin.Context) {
	repos, err := h.st.ListRepositories(c.Request.Context())
	if err != nil {
		h.internalError(c, err)
		return
	}
	out := make([]gin.H, 0, len(repos))
	for _, r := range repos {
		out = append(out, repoJSON(r))
	}
	c.JSON(http.StatusOK, gin.H{"repositories": out})
}

func (h *QueryHandler) ListCases(c *gin.Context) {
	cases, err := h.st.ListCases(c.Request.Context())
	if err != nil {
		h.internalError(c, err)
		return
	}
	out := make([]gin.H, 0, len(cases))
	for _, cs := range cases {
		out = append(out, gin.H{
			"id":           cs.ID,
			"repo_id":      cs.RepoID,
			"issue_number": cs.IssueNumber,
			"title":        cs.Title,
			"state":        cs.DisplayState,
			"updated_at":   cs.UpdatedAt.Time,
			"repo": gin.H{
				"owner":     cs.RepoOwner,
				"name":      cs.RepoName,
				"issue_url": fmt.Sprintf("https://github.com/%s/%s/issues/%d", cs.RepoOwner, cs.RepoName, cs.IssueNumber),
			},
		})
	}
	c.JSON(http.StatusOK, gin.H{"cases": out})
}

// GetCase 详情：case + runs 轨迹 + current 草稿 + 费用汇总。
func (h *QueryHandler) GetCase(c *gin.Context) {
	ctx := c.Request.Context()
	cs, err := h.st.GetCase(ctx, c.Param("caseID"))
	if errors.Is(err, store.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "case 不存在"})
		return
	}
	if err != nil {
		h.internalError(c, err)
		return
	}

	runs, err := h.st.ListRunsForCase(ctx, cs.ID)
	if err != nil {
		h.internalError(c, err)
		return
	}
	runsOut := make([]gin.H, 0, len(runs))
	for _, r := range runs {
		runsOut = append(runsOut, runJSON(r))
	}

	resp := gin.H{
		"id": cs.ID, "issue_number": cs.IssueNumber, "title": cs.Title,
		"state": cs.DisplayState, "runs": runsOut,
	}

	// current 草稿（若有）+ 费用
	if draft, err := h.st.GetCurrentDraftForCaseId(ctx, cs.ID); err == nil {
		detail := gin.H{
			"id":         draft.ID,
			"conclusion": draft.Conclusion,
			"evidence":   json.RawMessage(draft.Evidence),
		}
		if draft.NeedsInfoQuestions != nil {
			detail["needs_info_questions"] = json.RawMessage(draft.NeedsInfoQuestions)
		}
		// 最新 run 的费用汇总
		if len(runs) > 0 {
			calls, err := h.st.ListModelCallsForRun(ctx, runs[0].ID)
			if err == nil && len(calls) > 0 {
				var totalCost float64
				for _, mc := range calls {
					cost, _ := mc.CostCny.Float64Value()
					totalCost += cost.Float64
				}
				detail["cost_cny_total"] = totalCost
				detail["model_calls"] = len(calls)
			}
		}
		resp["draft"] = detail
	}
	c.JSON(http.StatusOK, resp)
}

// GetRun 详情：run + 提交轨迹 + 逐调用费用（审计视图）。
func (h *QueryHandler) GetRun(c *gin.Context) {
	ctx := c.Request.Context()
	r, err := h.st.GetRun(ctx, c.Param("runID"))
	if errors.Is(err, store.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "run 不存在"})
		return
	}
	if err != nil {
		h.internalError(c, err)
		return
	}

	commits, err := h.st.ListRunCommits(ctx, r.ID)
	if err != nil {
		h.internalError(c, err)
		return
	}
	commitsOut := make([]gin.H, 0, len(commits))
	for _, cm := range commits {
		commitsOut = append(commitsOut, gin.H{
			"commit_id": cm.CommitID, "stage": cm.Stage,
			"payload_hash": cm.PayloadHash, "lease_epoch": cm.LeaseEpoch,
			"result": json.RawMessage(cm.Result), "created_at": cm.CreatedAt.Time,
		})
	}

	calls, err := h.st.ListModelCallsForRun(ctx, r.ID)
	if err != nil {
		h.internalError(c, err)
		return
	}
	callsOut := make([]gin.H, 0, len(calls))
	for _, mc := range calls {
		cost, _ := mc.CostCny.Float64Value()
		callsOut = append(callsOut, gin.H{
			"model": mc.Model, "input_tokens": mc.InputTokens,
			"output_tokens": mc.OutputTokens,
			"cost_cny":      cost.Float64, "cost_status": mc.CostStatus,
			"created_at": mc.CreatedAt.Time,
		})
	}

	c.JSON(http.StatusOK, gin.H{"run": runJSON(r), "commits": commitsOut, "model_calls": callsOut})
}

// CancelRun：QUEUED → CANCELLED；RUNNING → CANCEL_REQUESTED（合作式）。
func (h *QueryHandler) CancelRun(c *gin.Context) {
	rows, err := h.st.RequestCancelRun(c.Request.Context(), c.Param("runID"))
	if err != nil {
		h.internalError(c, err)
		return
	}
	if rows == 0 {
		// 终态 run 不可取消——回显当前状态而非报错（幂等语义）
		if r, getErr := h.st.GetRun(c.Request.Context(), c.Param("runID")); getErr == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "run 已是终态", "status": r.Status})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "run 不存在"})
		return
	}
	r, err := h.st.GetRun(c.Request.Context(), c.Param("runID"))
	if err != nil {
		h.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": runJSON(r)})
}

// ---- 形状辅助 ----

func repoJSON(r db.Repository) gin.H {
	return gin.H{
		"id": r.ID, "repo_numeric_id": r.RepoNumericID,
		"owner": r.Owner, "name": r.Name,
		"default_branch": r.DefaultBranch, "installation_id": r.InstallationID,
		"capabilities":   json.RawMessage(r.Capabilities),
		"policy_version": r.PolicyVersion, "status": r.Status,
	}
}

func runJSON(r db.Run) gin.H {
	var finishedAt any
	if r.FinishedAt.Valid {
		finishedAt = r.FinishedAt.Time
	}
	return gin.H{
		"id": r.ID, "case_id": r.CaseID, "status": r.Status,
		"outcome": r.Outcome, "lease_epoch": r.LeaseEpoch,
		"attempts": r.Attempts, "model_calls_used": r.ModelCallsUsed,
		"error": r.Error, "created_at": r.CreatedAt.Time, "finished_at": finishedAt,
	}
}

func (h *QueryHandler) internalError(c *gin.Context, err error) {
	// 详情只进日志（可能含 SQL 错误文本），响应不带
	c.Set("internal_error", err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{"error": "内部错误"})
}

// ParseID 是路径参数转 int64 的便捷（供 onboarding 查询参数用）。
func ParseID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
