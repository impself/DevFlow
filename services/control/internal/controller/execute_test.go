package controller_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/runtimeclient"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/testutil"
)

// ---- 测试替身：内容预取与智能层 ----

// fakeContents 预取假实现：固定 SHA + 固定 README，不碰网络。
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
	return nil, nil // 不存在 = 正常形态
}

// fakeAnalyzer 智能层假实现：按脚本返回响应并记录调用次数。
type fakeAnalyzer struct {
	resp    []byte
	err     error
	calls   atomic.Int32
	lastReq *runtimeclient.AnalysisRequest
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, req runtimeclient.AnalysisRequest) ([]byte, error) {
	f.calls.Add(1)
	f.lastReq = &req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

const analyzerAnswerReady = `{
  "run_id": "__RUN__", "outcome": "ANSWER_READY",
  "reply_markdown": "分页从 0 开始，见 README 第 3 行。",
  "evidence": [{"source_type":"doc_file","location":{"path":"README.md","sha":"deadbee","line_start":3,"line_end":3},"quote":"pages start at 0"}],
  "degraded": false,
  "usage": {"model":"qwen-plus","input_tokens":3200,"output_tokens":450,"cost_cny":0.012,"cost_status":"estimated"}
}`

// executeFixture 迁移清场 + 种 run + 领取，返回执行所需的全部件。
func executeFixture(t *testing.T) (*store.Store, controller.Claim, *fakeContents, *controller.ArtifactStore, string) {
	pool, dbURL := testutil.NewPool(t)
	testutil.Migrate(t, pool)
	testutil.Reset(t, pool)
	testutil.SeedRun(t, pool, "exec")

	st, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)

	owner := "worker-exec"
	claim, err := st.ClaimRun(t.Context(), &owner)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}
	dir := t.TempDir()
	return st, claim, &fakeContents{
		head: "fixed0sha",
		files: map[string]*github.FileContent{
			"README.md": {Path: "README.md", SHA: "deadbee", Size: 44, Content: []byte("first line\nsecond line\npages start at 0")},
		},
	}, controller.NewArtifactStore(st, dir), dir
}

func TestExecute(t *testing.T) {
	t.Run("ANSWER_READY 全流程：合同校验+费用记录+原子提交", func(t *testing.T) {
		st, claim, contents, artifacts, _ := executeFixture(t)
		analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim.ID, 1))}
		exec := controller.NewRunExecutor(st, contents, analyzer, "worker-exec", artifacts)

		outcome, err := exec.Execute(t.Context(), claim)
		if err != nil {
			t.Fatalf("执行失败: %v", err)
		}
		if outcome != "ANSWER_READY" {
			t.Fatalf("outcome = %s", outcome)
		}

		pool := st.Pool()
		var status, runOutcome string
		if err := pool.QueryRow(t.Context(),
			`SELECT status, outcome FROM runs WHERE id=$1`, claim.ID).Scan(&status, &runOutcome); err != nil ||
			status != "COMPLETED" || runOutcome != "ANSWER_READY" {
			t.Fatalf("run 应 COMPLETED/ANSWER_READY: %s/%s err=%v", status, runOutcome, err)
		}
		if got := receiptCount(t, pool, claim.ID); got != 1 {
			t.Fatalf("应有 1 条提交回执，得到 %d", got)
		}
		// 回执里的 result 与 payload_hash 一致性由 commit 协议保证；这里验证预算计数
		var used int
		if err := pool.QueryRow(t.Context(),
			`SELECT model_calls_used FROM runs WHERE id=$1`, claim.ID).Scan(&used); err != nil || used != 1 {
			t.Fatalf("model_calls_used 应为 1: %d err=%v", used, err)
		}
		if got := count(t, pool, `SELECT count(*) FROM model_calls WHERE run_id=$1`, claim.ID); got != 1 {
			t.Fatalf("应有 1 条费用记录，得到 %d", got)
		}
		// 出站请求必须携带固定 head SHA 与预取文件（FR-3）
		if analyzer.lastReq == nil || analyzer.lastReq.Repo.HeadSHA != "fixed0sha" || len(analyzer.lastReq.Files) != 1 {
			t.Fatalf("出站请求缺少固定 SHA 或预取文件: %+v", analyzer.lastReq)
		}
	})

	t.Run("智能层响应违反合同：执行失败且不落终态", func(t *testing.T) {
		st, claim, contents, artifacts, _ := executeFixture(t)
		// ANSWER_READY 缺 reply_markdown → schema 拒绝
		bad := strings.Replace(`{"run_id":"__RUN__","outcome":"ANSWER_READY","evidence":[{"source_type":"doc_file","location":{"path":"README.md","sha":"deadbee"}}],"usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0.01,"cost_status":"estimated"}}`, "__RUN__", claim.ID, 1)
		analyzer := &fakeAnalyzer{resp: []byte(bad)}
		exec := controller.NewRunExecutor(st, contents, analyzer, "worker-exec", artifacts)

		if _, err := exec.Execute(t.Context(), claim); err == nil {
			t.Fatal("违反合同的响应应导致执行失败")
		}
		var status string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT status FROM runs WHERE id=$1`, claim.ID).Scan(&status); err != nil || status != "RUNNING" {
			t.Fatalf("失败后 run 应保持 RUNNING（由 worker 落 FAILED），得到 %s err=%v", status, err)
		}
	})

	t.Run("智能层不可用：错误上抛交给 worker 重试路径", func(t *testing.T) {
		st, claim, contents, artifacts, _ := executeFixture(t)
		analyzer := &fakeAnalyzer{err: runtimeclient.ErrUnavailable}
		exec := controller.NewRunExecutor(st, contents, analyzer, "worker-exec", artifacts)

		if _, err := exec.Execute(t.Context(), claim); !errors.Is(err, runtimeclient.ErrUnavailable) {
			t.Fatalf("应上抛 ErrUnavailable，得到 %v", err)
		}
	})

	t.Run("预算耗尽：LIMIT_REACHED 且不调智能层", func(t *testing.T) {
		st, claim, contents, artifacts, _ := executeFixture(t)
		if _, err := st.Pool().Exec(t.Context(),
			`UPDATE runs SET model_calls_used = max_model_calls WHERE id=$1`, claim.ID); err != nil {
			t.Fatalf("耗尽预算: %v", err)
		}
		analyzer := &fakeAnalyzer{resp: []byte(`{}`)}
		exec := controller.NewRunExecutor(st, contents, analyzer, "worker-exec", artifacts)

		outcome, err := exec.Execute(t.Context(), claim)
		if err != nil || outcome != "LIMIT_REACHED" {
			t.Fatalf("应 LIMIT_REACHED: %s %v", outcome, err)
		}
		if analyzer.calls.Load() != 0 {
			t.Fatal("预算耗尽后不得再调智能层")
		}
		var status string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT status FROM runs WHERE id=$1`, claim.ID).Scan(&status); err != nil || status != "COMPLETED" {
			t.Fatalf("run 应 COMPLETED(LIMIT_REACHED): %s err=%v", status, err)
		}
	})
}
