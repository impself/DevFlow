package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	githubpkg "github.com/google/go-github/v90/github"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/publisher"
	"github.com/impself/DevFlow/services/control/internal/runtimeclient"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// ---- 假 GitHub：可导演「发布成功 / 明确拒绝 / 响应丢失」三种世界 ----

type fakeOps struct {
	mu          sync.Mutex
	issueState  string
	comments    []publisher.Comment
	createErr   error // CreateComment 的错误（导演网络黑洞 / 4xx 明确拒绝）
	listErr     error // ListComments 的错误（导演 preflight infra 故障）
	createCalls int
	// silentPost：CreateComment 实际成功但返回网络错误（响应丢失）——
	// 评论已在远端，靠 reconcile 找回来。
	silentPost    bool
	nextCommentID int64
}

func (f *fakeOps) GetIssue(ctx context.Context, number int64) (*publisher.IssueInfo, error) {
	return &publisher.IssueInfo{State: f.issueState}, nil
}

func (f *fakeOps) ListComments(ctx context.Context, number int64, opts publisher.ListOpts) ([]publisher.Comment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if opts.Since == nil {
		return f.comments, nil
	}
	// 尊重服务端过滤语义：只返回 Since 之后的评论
	out := make([]publisher.Comment, 0, len(f.comments))
	for _, c := range f.comments {
		if c.CreatedAt.After(*opts.Since) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeOps) CreateComment(ctx context.Context, number int64, body string) (int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return 0, "", f.createErr
	}
	f.nextCommentID++
	id := f.nextCommentID
	url := fmt.Sprintf("https://github.com/o/n/issues/7#issuecomment-%d", id)
	f.comments = append(f.comments, publisher.Comment{
		ID: id, URL: url, Author: "devflow-bot[bot]", Body: body, CreatedAt: time.Now(),
	})
	if f.silentPost {
		// 实际发成功，但响应丢失（模拟超时）
		return 0, "", errors.New("POST https://api.github.com: context deadline exceeded")
	}
	return id, url, nil
}

type fakeResolver struct{ ops *fakeOps }

func (r *fakeResolver) ForRepo(ctx context.Context, repoNumericID int64) (*publisher.ResolvedClient, error) {
	return &publisher.ResolvedClient{Owner: "o", Name: "n", Ops: r.ops}, nil
}

// ---- 本包自带的执行流假实现（与 controller_test 同构但独立，避免跨测试包耦合）----

type fakeContents struct {
	head  string
	files map[string]*github.FileContent
}

func (f *fakeContents) HeadSHA(ctx context.Context, installationID int64, owner, name, ref string) (string, error) {
	return f.head, nil
}

func (f *fakeContents) FileAt(ctx context.Context, installationID int64, owner, name, ref, path string) (*github.FileContent, error) {
	if fc, ok := f.files[path]; ok {
		return fc, nil
	}
	return nil, nil
}

type fakeAnalyzer struct {
	resp []byte
	err  error
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, req runtimeclient.AnalysisRequest) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

const answerReadyTmpl = `{
  "run_id": "__RUN__", "outcome": "ANSWER_READY",
  "reply_markdown": "分页从 0 开始，见 README 第 3 行。",
  "evidence": [{"source_type":"doc_file","location":{"path":"README.md","sha":"deadbee","line_start":3,"line_end":3},"quote":"pages start at 0"}],
  "degraded": false,
  "usage": {"model":"qwen-plus","input_tokens":3200,"output_tokens":450,"cost_cny":0.012,"cost_status":"estimated"}
}`

// publishFixture：完成「分析 → 审批包 → 批准」的现场。
func publishFixture(t *testing.T, ops *fakeOps) (*store.Store, string, *publisher.Publisher) {
	t.Helper()
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	testutil.SeedRun(t, pool, "pub")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	owner := "worker-pub"
	claim, err := st.ClaimRun(t.Context(), &owner)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}
	contents := &fakeContents{
		head: "fixed0sha",
		files: map[string]*github.FileContent{
			"README.md": {Path: "README.md", SHA: "deadbee", Size: 44, Content: []byte("first line\nsecond line\npages start at 0")},
		},
	}
	dir := t.TempDir()
	artifacts := controller.NewArtifactStore(st, dir)
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(answerReadyTmpl, "__RUN__", claim.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, owner, artifacts)
	if _, err := exec.Execute(t.Context(), claim); err != nil {
		t.Fatalf("执行: %v", err)
	}
	svc := controller.NewApprovalService(st)
	b, err := svc.CreateBundle(t.Context(), claim.CaseID)
	if err != nil {
		t.Fatalf("创建审批包: %v", err)
	}
	if _, err := svc.Approve(t.Context(), b.ID, "operator"); err != nil {
		t.Fatalf("批准: %v", err)
	}
	return st, b.ID, publisher.NewPublisher(st, &fakeResolver{ops: ops}, dir)
}

func bundleStatus(t *testing.T, st *store.Store, bundleID string) string {
	t.Helper()
	var s string
	if err := st.Pool().QueryRow(t.Context(),
		`SELECT status FROM approval_bundles WHERE id=$1`, bundleID).Scan(&s); err != nil {
		t.Fatalf("查 bundle: %v", err)
	}
	return s
}

func TestPublish(t *testing.T) {
	t.Run("正常发布：SUCCEEDED+回执锚点+bundle EXECUTED", func(t *testing.T) {
		ops := &fakeOps{issueState: "open"}
		st, bundleID, pub := publishFixture(t, ops)

		act, err := pub.Publish(t.Context(), bundleID)
		if err != nil {
			t.Fatalf("发布失败: %v", err)
		}
		if act.Status != "SUCCEEDED" || act.RemoteCommentID == nil {
			t.Fatalf("应 SUCCEEDED 带 remote 锚点: %s", act.Status)
		}
		if ops.createCalls != 1 {
			t.Fatalf("应恰好发一条评论，发了 %d 条", ops.createCalls)
		}
		if got := bundleStatus(t, st, bundleID); got != "EXECUTED" {
			t.Fatalf("bundle 应 EXECUTED，得到 %s", got)
		}
		// 回执关键字段（action-receipt 合同的核心）——结构化解码，不看排版
		var receipt map[string]any
		if err := json.Unmarshal(act.Receipt, &receipt); err != nil {
			t.Fatalf("回执应为 JSON: %v", err)
		}
		for _, key := range []string{"status", "action_id", "bundle_id", "remote_comment_id", "remote_url", "checked_at"} {
			if _, ok := receipt[key]; !ok {
				t.Fatalf("回执缺字段 %s: %s", key, act.Receipt)
			}
		}
		if receipt["status"] != "SUCCEEDED" {
			t.Fatalf("回执状态应为 SUCCEEDED: %v", receipt["status"])
		}
	})

	t.Run("重复发布：回显现有 action，绝不发第二条", func(t *testing.T) {
		ops := &fakeOps{issueState: "open"}
		_, bundleID, pub := publishFixture(t, ops)
		first, err := pub.Publish(t.Context(), bundleID)
		if err != nil {
			t.Fatalf("首次发布: %v", err)
		}
		act, err := pub.Publish(t.Context(), bundleID)
		if !errors.Is(err, publisher.ErrActionAlreadyMoved) {
			t.Fatalf("重复发布应报已推进，得到 %v", err)
		}
		if act.ID != first.ID || act.Status != "SUCCEEDED" {
			t.Fatalf("应回显同一 SUCCEEDED action: %s/%s", act.ID, act.Status)
		}
		if ops.createCalls != 1 {
			t.Fatalf("重复发布不得再发评论，发了 %d 条", ops.createCalls)
		}
	})

	t.Run("重核拦截：Issue 已关闭 → bundle EXPIRED + action FAILED（FR-8）", func(t *testing.T) {
		ops := &fakeOps{issueState: "closed"}
		st, bundleID, pub := publishFixture(t, ops)

		_, err := pub.Publish(t.Context(), bundleID)
		if err == nil || !strings.Contains(err.Error(), "已关闭") {
			t.Fatalf("应报 Issue 已关闭: %v", err)
		}
		if ops.createCalls != 0 {
			t.Fatalf("重核失败不得发评论，发了 %d 条", ops.createCalls)
		}
		if got := bundleStatus(t, st, bundleID); got != "EXPIRED" {
			t.Fatalf("bundle 应 EXPIRED，得到 %s", got)
		}
		var aStatus string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT status FROM actions WHERE bundle_id=$1`, bundleID).Scan(&aStatus); err != nil || aStatus != "FAILED" {
			t.Fatalf("action 应 FAILED: %s err=%v", aStatus, err)
		}
	})

	t.Run("响应丢失：reconcile 命中 → SUCCEEDED，只发一条（AC36）", func(t *testing.T) {
		ops := &fakeOps{issueState: "open", silentPost: true}
		_, bundleID, pub := publishFixture(t, ops)

		act, err := pub.Publish(t.Context(), bundleID) // 内部自动进入核对
		if err != nil {
			t.Fatalf("核对命中后应成功: %v", err)
		}
		if act.Status != "SUCCEEDED" || act.RemoteCommentID == nil {
			t.Fatalf("核对应找回评论: %s", act.Status)
		}
		if ops.createCalls != 1 {
			t.Fatalf("响应丢失后不得重发，发了 %d 条", ops.createCalls)
		}
	})

	t.Run("响应丢失且核对不到：保持 RECONCILING 转人工（绝不自动重发）", func(t *testing.T) {
		ops := &fakeOps{
			issueState: "open",
			createErr:  errors.New("POST https://api.github.com: context deadline exceeded"),
		}
		_, bundleID, pub := publishFixture(t, ops)

		act, err := pub.Publish(t.Context(), bundleID)
		if !errors.Is(err, publisher.ErrNeedsManualDecision) {
			t.Fatalf("应转人工决策，得到 %v", err)
		}
		if act.Status != "RECONCILING" {
			t.Fatalf("应保持 RECONCILING: %s", act.Status)
		}
		if ops.createCalls != 1 {
			t.Fatalf("系统只应尝试一次，发了 %d 条", ops.createCalls)
		}
	})

	t.Run("GitHub 明确拒绝（4xx）：直接 FAILED，不进核对", func(t *testing.T) {
		ops := &fakeOps{
			issueState: "open",
			createErr: &githubpkg.ErrorResponse{
				Response: &http.Response{StatusCode: 422},
				Message:  "Invalid request",
			},
		}
		st, bundleID, pub := publishFixture(t, ops)

		_, err := pub.Publish(t.Context(), bundleID)
		if err == nil || !strings.Contains(err.Error(), "明确拒绝") {
			t.Fatalf("应标记明确失败: %v", err)
		}
		if ops.createCalls != 1 {
			t.Fatalf("明确失败不得重试，发了 %d 条", ops.createCalls)
		}
		var aStatus, bStatus string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT status FROM actions WHERE bundle_id=$1`, bundleID).Scan(&aStatus); err != nil || aStatus != "FAILED" {
			t.Fatalf("action 应 FAILED: %s err=%v", aStatus, err)
		}
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT status FROM approval_bundles WHERE id=$1`, bundleID).Scan(&bStatus); err != nil || bStatus != "EXPIRED" {
			t.Fatalf("bundle 应同步 EXPIRED: %s err=%v", bStatus, err)
		}
	})

	t.Run("重核哨兵：批准后新人类回复 → EXPIRED 零评论", func(t *testing.T) {
		ops := &fakeOps{issueState: "open"}
		time.Sleep(10 * time.Millisecond) // 让 bundle 创建时间落在预置评论之后
		st, bundleID, pub := publishFixture(t, ops)
		// bundle 创建之后出现的人类回复（Since 服务端过滤应捕获它）
		ops.mu.Lock()
		ops.comments = append(ops.comments, publisher.Comment{
			ID: 2, Author: "bob", Body: "批准后插话", CreatedAt: time.Now().Add(1 * time.Second),
		})
		ops.mu.Unlock()

		_, err := pub.Publish(t.Context(), bundleID)
		if !errors.Is(err, publisher.ErrNewHumanReply) {
			t.Fatalf("应 ErrNewHumanReply: %v", err)
		}
		if ops.createCalls != 0 {
			t.Fatalf("重核失败不得发评论，发了 %d 条", ops.createCalls)
		}
		if got := bundleStatus(t, st, bundleID); got != "EXPIRED" {
			t.Fatalf("bundle 应 EXPIRED，得到 %s", got)
		}
	})

	t.Run("重核基础设施错误：不作废审批，可重试后成功（review P1-5）", func(t *testing.T) {
		ops := &fakeOps{issueState: "open", listErr: errors.New("503 upstream unavailable")}
		st, bundleID, pub := publishFixture(t, ops)

		_, err := pub.Publish(t.Context(), bundleID)
		if err == nil || !strings.Contains(err.Error(), "无法完成") {
			t.Fatalf("应报无法核实: %v", err)
		}
		if got := bundleStatus(t, st, bundleID); got != "EXECUTING" {
			t.Fatalf("infra 错误不得作废 bundle，得到 %s", got)
		}
		ops.mu.Lock()
		ops.listErr = nil
		ops.mu.Unlock()
		act, err := pub.Publish(t.Context(), bundleID)
		if err != nil || act.Status != "SUCCEEDED" {
			t.Fatalf("恢复后应发布成功: %v %s", err, act.Status)
		}
	})

	t.Run("429 限流：不判死刑，进核对后转人工（review P1-4）", func(t *testing.T) {
		ops := &fakeOps{
			issueState: "open",
			createErr: &githubpkg.ErrorResponse{
				Response: &http.Response{StatusCode: 429},
				Message:  "rate limited",
			},
		}
		_, bundleID, pub := publishFixture(t, ops)

		act, err := pub.Publish(t.Context(), bundleID)
		if !errors.Is(err, publisher.ErrNeedsManualDecision) {
			t.Fatalf("限流不应判死刑: %v", err)
		}
		if act.Status != "RECONCILING" {
			t.Fatalf("应保持 RECONCILING: %s", act.Status)
		}
	})

	t.Run("中断恢复：EXECUTING 的 action 重入先核对，命中即成功（review P1-1）", func(t *testing.T) {
		ops := &fakeOps{issueState: "open", silentPost: true}
		st, bundleID, pub := publishFixture(t, ops)

		if _, err := pub.Publish(t.Context(), bundleID); err != nil {
			t.Fatalf("第一次发布: %v", err)
		}
		// 模拟「执行中崩溃」：action 与 bundle 都停在中间态后重入
		if _, err := st.Pool().Exec(t.Context(),
			`UPDATE actions SET status='EXECUTING' WHERE bundle_id=$1`, bundleID); err != nil {
			t.Fatalf("模拟中断: %v", err)
		}
		if _, err := st.Pool().Exec(t.Context(),
			`UPDATE approval_bundles SET status='EXECUTING' WHERE id=$1`, bundleID); err != nil {
			t.Fatalf("模拟中断(bundle): %v", err)
		}
		act, err := pub.Publish(t.Context(), bundleID)
		if err != nil || act.Status != "SUCCEEDED" {
			t.Fatalf("中断恢复应核对命中: %v %s", err, act.Status)
		}
		if ops.createCalls != 1 {
			t.Fatalf("恢复不得重发评论，发了 %d 条", ops.createCalls)
		}
	})
}
