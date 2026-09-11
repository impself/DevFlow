package controller

// Run 执行流（T014）：领取后的完整生命周期——
// 预算检查 → 预取固定 SHA 内容 → 调智能层 → 合同复验 → 记费用 → 原子提交。
// 每一步失败都有明确归宿：执行错误 → worker 走 FAILED；协议错误 → 租约保护。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/impself/DevFlow/services/control/internal/contract"
	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/runtimeclient"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// 执行预算与预取上限（plan.md Constraints；参数如需配置化，进 config 而非常量散落）。
const (
	runBudget        = 900 * time.Second // 单 Run ≤900 秒
	analyzeTimeout   = 300 * time.Second // 单次智能层调用兜底（模型慢响应防线）
	maxPrefetchFiles = 5                 // ≤N 个文件
	maxFileBytes     = 1 << 20           // 单文件 1MB（超出跳过，Contents API 内联上限同级）
	maxTotalBytes    = 2 << 20           // 总量 2MB（prompt 注入成本防线）
)

// Analyzer 是智能层的抽象：生产是 runtimeclient.Client，测试用假实现。
// 返回原始字节——schema 复验是执行器的职责（合同优先，不信任出站层）。
type Analyzer interface {
	Analyze(ctx context.Context, req runtimeclient.AnalysisRequest) ([]byte, error)
}

// RunExecutor 编排一次 run 的执行。
type RunExecutor struct {
	st        *store.Store
	contents  github.RepoContentProvider
	analyzer  Analyzer
	owner     string // 与 worker 相同的租约身份，提交时用于 epoch 校验
	artifacts *ArtifactStore
}

func NewRunExecutor(st *store.Store, contents github.RepoContentProvider, analyzer Analyzer,
	owner string, artifacts *ArtifactStore) *RunExecutor {
	return &RunExecutor{st: st, contents: contents, analyzer: analyzer, owner: owner, artifacts: artifacts}
}

// issueSnapshot 是 webhook 写入 input_snapshot 的结构镜像（T011）。
type issueSnapshot struct {
	Issue struct {
		Number int64  `json:"number"`
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"issue"`
	Repo struct {
		NumericID     int64  `json:"numeric_id"`
		Owner         string `json:"owner"`
		Name          string `json:"name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repo"`
}

// Execute 是 worker 的执行钩子：返回 outcome 给 worker 落账以外，
// 终态提交在本函数内通过租约协议完成（worker 不重复写）。
func (e *RunExecutor) Execute(ctx context.Context, claim Claim) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runBudget)
	defer cancel()

	// 1. 预算护栏：模型调用额度耗尽 → 直接提交 LIMIT_REACHED，不再花钱
	run, err := e.st.GetRun(ctx, claim.ID)
	if err != nil {
		return "", fmt.Errorf("读取 run: %w", err)
	}
	if run.ModelCallsUsed >= run.MaxModelCalls {
		slog.Warn("模型调用预算耗尽", "run", claim.ID, "used", run.ModelCallsUsed)
		return "LIMIT_REACHED", e.commit(ctx, claim, "LIMIT_REACHED", nil)
	}

	// 2. case + 仓库信息（预取与请求装配都需要）
	cs, err := e.st.GetCase(ctx, claim.CaseID)
	if err != nil {
		return "", fmt.Errorf("读取 case: %w", err)
	}
	repo, err := e.st.GetRepository(ctx, cs.RepoID)
	if err != nil {
		return "", fmt.Errorf("读取 repository: %w", err)
	}

	// 3. 解析触发时刻的固定快照（FR-3：以快照为准，不读实时 Issue）
	var snap issueSnapshot
	if err := json.Unmarshal(run.InputSnapshot, &snap); err != nil {
		return "", fmt.Errorf("解析 input_snapshot: %w", err)
	}

	// 4. 预取固定 head SHA 的内容（≤N 文件、大小限额）
	headSHA, files, err := e.prefetch(ctx, repo, snap)
	if err != nil {
		return "", fmt.Errorf("预取仓库内容: %w", err)
	}

	// 5. 调智能层（带兜底超时）
	analyzerCtx, analyzerCancel := context.WithTimeout(ctx, analyzeTimeout)
	req := runtimeclient.AnalysisRequest{
		RunID: claim.ID,
		Issue: runtimeclient.IssueInput{
			Number: snap.Issue.Number, Title: snap.Issue.Title,
			Body: snap.Issue.Body, Author: issueAuthor(run.InputSnapshot),
		},
		Repo: runtimeclient.RepoInput{
			NumericID: repo.RepoNumericID,
			FullName:  repo.Owner + "/" + repo.Name,
			HeadSHA:   headSHA,
		},
		Files: files,
	}
	raw, err := e.analyzer.Analyze(analyzerCtx, req)
	analyzerCancel()
	if err != nil {
		return "", fmt.Errorf("智能层调用失败: %w", err)
	}

	// 6. 合同复验：智能层返回必须过 schema（防御式信任边界）
	out, err := contract.ValidateIssueAgentOutput(raw)
	if err != nil {
		return "", err
	}
	if out.RunID != claim.ID {
		return "", fmt.Errorf("智能层返回的 run_id 不匹配: got %s want %s", out.RunID, claim.ID)
	}

	// 6.5 引用逐字门（调研 P0-2，对齐 Gemini Citation Faithfulness Check）：
	// 0-token 纯机械核对——quote 必须逐字出现在预取文件里，行号必须框得住 quote。
	// 防三类失败：fabricated（编造引用）、frankenquote（拼接引用）、编造行号。
	// 未命中条目剔除；ANSWER_READY 失去全部证据 → 降级 NEEDS_INFO（弃答优于编造）。
	commitPayload := raw
	if gateEvidence(&out, files) {
		modified, mErr := json.Marshal(out)
		if mErr == nil {
			commitPayload = modified
		}
		slog.Warn("引用逐字门拦截：证据被剔除或降级", "run", claim.ID, "outcome", out.Outcome)
	}

	// 7. 记费用（FR-10/AC47）：失败只记日志，不因记账失败毁掉一次成功的分析
	e.recordUsage(ctx, claim.ID, out.Usage)

	// 7.5 产物落库（草稿/证据/原始输出）：失败只记日志——产物缺席时
	// run_commits.result 里仍有完整 JSON（取证兜底），不值得因此毁掉一次成功分析。
	// 落库失败的草稿不会进入审批流（case 查不到 current 草稿）。
	if e.artifacts != nil {
		if err := e.artifacts.SaveDraft(ctx, claim.ID, claim.CaseID, &out, raw); err != nil {
			slog.Error("产物落库失败", "run", claim.ID, "err", err)
		}
	}

	// 8. 原子提交（AC23–26）：回执 + 终态同事务。
	// 提交的是逐字门之后的规范化结果；原始模型输出已在 raw_model_io 留档。
	if err := e.commit(ctx, claim, out.Outcome, commitPayload); err != nil {
		return "", err
	}
	return out.Outcome, nil
}

// commit 走租约协议提交。commit_id 由 (stage, run) 结构化派生：
// 同一 run 同一阶段的重试天然同 ID——幂等键来自业务结构（T013 笔记 §6）。
func (e *RunExecutor) commit(ctx context.Context, claim Claim, outcome string, raw []byte) error {
	commitID := "issue-analysis-" + claim.ID
	payloadHash := ""
	if raw != nil {
		sum := sha256.Sum256(raw)
		payloadHash = hex.EncodeToString(sum[:])
	}
	if raw == nil { // LIMIT_REACHED 等无模型产出的终态：result 记空对象占位
		raw = []byte(`{}`)
		sum := sha256.Sum256(raw)
		payloadHash = hex.EncodeToString(sum[:])
	}
	_, err := CommitRunResult(ctx, e.st, e.owner, claim, commitID, payloadHash, raw, outcome)
	return err
}

// prefetch 拉取固定 head SHA 上的分析输入文件。
// M1 预取 README.md：后续扩展配置化文件清单（如 docs/**）。
func (e *RunExecutor) prefetch(ctx context.Context, repo db.Repository, snap issueSnapshot) (string, []runtimeclient.File, error) {
	head, err := e.contents.HeadSHA(ctx, repo.InstallationID, repo.Owner, repo.Name, repo.DefaultBranch)
	if err != nil {
		return "", nil, err
	}

	files := make([]runtimeclient.File, 0, maxPrefetchFiles)
	total := 0
	for _, path := range []string{"README.md"} {
		fc, err := e.contents.FileAt(ctx, repo.InstallationID, repo.Owner, repo.Name, head, path)
		if err != nil {
			return "", nil, fmt.Errorf("预取 %s: %w", path, err)
		}
		if fc == nil || fc.Content == nil {
			continue // 不存在或超单文件上限
		}
		if len(fc.Content) > maxFileBytes || total+len(fc.Content) > maxTotalBytes {
			slog.Warn("预取文件超限跳过", "path", path, "size", len(fc.Content))
			continue
		}
		total += len(fc.Content)
		files = append(files, runtimeclient.File{Path: path, Content: string(fc.Content)})
	}
	return head, files, nil
}

// recordUsage 把 usage 落 model_calls 并递增 run 预算计数（AC47：不记零由 DB CHECK 兜底）。
func (e *RunExecutor) recordUsage(ctx context.Context, runID string, u contract.Usage) {
	err := e.st.WithTx(ctx, func(q *db.Queries) error {
		// numeric 列经字符串扫描进 pgtype.Numeric，避免 float 直接构造的精度漂移
		var cost pgtype.Numeric
		if err := cost.Scan(fmt.Sprintf("%.4f", u.CostCNY)); err != nil {
			return fmt.Errorf("解析费用 %v: %w", u.CostCNY, err)
		}
		if err := q.InsertModelCall(ctx, db.InsertModelCallParams{
			ID:           ids.New("mc"),
			RunID:        runID,
			Model:        u.Model,
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostCny:      cost,
			CostStatus:   u.CostStatus,
		}); err != nil {
			return err
		}
		_, err := q.IncrementModelCallsUsed(ctx, runID)
		return err
	})
	if err != nil {
		slog.Error("记录模型费用失败", "run", runID, "err", err)
	}
}

// gateEvidence 原地核对并修正 out 的 evidence；返回是否发生了降级。
// 规则（0 token，纯 Go）：
//   - quote 空或预取文件里找不到 path → 剔除该条；
//   - 给了行号：quote（空白归一后）必须是该行区间内容的子串——防编造行号；
//   - ANSWER_READY 剔完后无证据 → 降级 NEEDS_INFO（degraded=true）。
//
// 空白归一（折叠连续空白）对齐 Gemini notebook 的 normalize：容忍换行/缩进差异，
// 不容忍内容差异。
func gateEvidence(out *contract.AgentOutput, files []runtimeclient.File) bool {
	byPath := make(map[string]string, len(files))
	for _, f := range files {
		byPath[f.Path] = f.Content
	}

	kept := out.Evidence[:0]
	for _, ev := range out.Evidence {
		if !evidenceMatches(ev, byPath[ev.Location.Path]) {
			continue
		}
		kept = append(kept, ev)
	}
	out.Evidence = kept

	if out.Outcome == "ANSWER_READY" && len(kept) == 0 {
		out.Outcome = "NEEDS_INFO"
		out.Degraded = true
		out.ReplyMarkdown = ""
		out.NeedsInfoQuestions = []string{
			"（系统降级）模型给出的引用未能通过逐字核对，请补充更具体的问题描述。",
		}
		return true
	}
	return len(kept) != len(out.Evidence) || false
}

// evidenceMatches 核对单条证据：quote 必须逐字（空白归一）出现在 content 中；
// 带行号时进一步要求落在 [line_start, line_end] 区间内。
func evidenceMatches(ev contract.Evidence, content string) bool {
	quote := normalizeWS(ev.Quote)
	if quote == "" || content == "" {
		return false // fail-closed：空 quote / 缺文件一律不认（调研原则）
	}
	if !strings.Contains(normalizeWS(content), quote) {
		return false // fabricated / frankenquote
	}
	if ev.Location.LineStart > 0 {
		lines := strings.Split(content, "\n")
		start, end := int(ev.Location.LineStart), int(ev.Location.LineEnd)
		if end < start {
			end = start
		}
		if start < 1 || end > len(lines) {
			return false // 行号越界 = 编造
		}
		span := strings.Join(lines[start-1:end], "\n")
		if !strings.Contains(normalizeWS(span), quote) {
			return false // quote 存在但不在声称的行区间
		}
	}
	return true
}

// normalizeWS 折叠连续空白为单个空格（引用比对的容差下限）。
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// issueAuthor 从快照补充字段里取 author（T011 之后的快照有；旧快照容忍为空）。
func issueAuthor(snapshot json.RawMessage) string {
	var probe struct {
		Issue struct {
			Author string `json:"author"`
		} `json:"issue"`
	}
	_ = json.Unmarshal(snapshot, &probe)
	return probe.Issue.Author
}
