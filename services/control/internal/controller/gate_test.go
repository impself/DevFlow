package controller_test

// 引用逐字门（verbatim gate，调研 P0-2）的测试：
// 防编造引用（fabricated）、拼接引用（frankenquote）、编造行号三类失败，
// 全部拦截后 ANSWER_READY 降级 NEEDS_INFO（弃答优于编造）。

import (
	"strings"
	"testing"

	"github.com/impself/DevFlow/services/control/internal/controller"
)

// gateFixture 复用 executeFixture 但允许自定义分析器响应。
func runWithResponse(t *testing.T, resp string) (string, error) {
	st, claim, contents, artifacts, _ := executeFixture(t)
	// 预取文件内容与 analyzerAnswerReady 的证据对齐已由 fixture 保证
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(resp, "__RUN__", claim.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, "worker-exec", artifacts)
	outcome, err := exec.Execute(t.Context(), claim)
	if err != nil {
		return "", err
	}
	// 回查落库的 result（逐字门之后的规范化结果）
	var resultStr string
	if err := st.Pool().QueryRow(t.Context(),
		`SELECT result::text FROM run_commits WHERE run_id=$1`, claim.ID).Scan(&resultStr); err != nil {
		t.Fatalf("查回执: %v", err)
	}
	return outcome + "|" + resultStr, nil
}

func TestVerbatimGate(t *testing.T) {
	t.Run("合法引用通过（quote 在文件内且行号框得住）", func(t *testing.T) {
		got, err := runWithResponse(t, analyzerAnswerReady)
		if err != nil {
			t.Fatalf("执行: %v", err)
		}
		if !strings.HasPrefix(got, "ANSWER_READY|") {
			t.Fatalf("合法引用不应降级: %s", got[:40])
		}
	})

	t.Run("编造引用（quote 不在文件中）→ 降级 NEEDS_INFO", func(t *testing.T) {
		fabricated := strings.Replace(analyzerAnswerReady,
			`"quote":"pages start at 0"`,
			`"quote":"这段话根本不在文件里"`, 1)
		got, err := runWithResponse(t, fabricated)
		if err != nil {
			t.Fatalf("执行: %v", err)
		}
		if !strings.HasPrefix(got, "NEEDS_INFO|") || !strings.Contains(got, `"degraded": true`) {
			t.Fatalf("应降级 NEEDS_INFO+degraded: %.120s", got)
		}
	})

	t.Run("编造行号（quote 在文件但行号越界）→ 降级", func(t *testing.T) {
		wrongLines := strings.Replace(analyzerAnswerReady,
			`"line_start":3,"line_end":3`,
			`"line_start":99,"line_end":100`, 1)
		got, err := runWithResponse(t, wrongLines)
		if err != nil {
			t.Fatalf("执行: %v", err)
		}
		if !strings.HasPrefix(got, "NEEDS_INFO|") {
			t.Fatalf("行号越界应降级: %.80s", got)
		}
	})

	t.Run("空白归一容差：换行/多空格差异不算编造", func(t *testing.T) {
		tolerant := strings.Replace(analyzerAnswerReady,
			`"quote":"pages start at 0"`,
			`"quote":"pages  start\n\tat 0"`, 1)
		got, err := runWithResponse(t, tolerant)
		if err != nil {
			t.Fatalf("执行: %v", err)
		}
		if !strings.HasPrefix(got, "ANSWER_READY|") {
			t.Fatalf("空白差异不应拦截: %.80s", got)
		}
	})

	t.Run("降级路径 NEEDS_INFO 不受门影响（无证据要求）", func(t *testing.T) {
		needsInfo := `{
		  "run_id": "__RUN__", "outcome": "NEEDS_INFO",
		  "needs_info_questions": ["版本号是？"],
		  "usage": {"model":"qwen-plus","input_tokens":10,"output_tokens":5,"cost_cny":0.001,"cost_status":"estimated"}
		}`
		got, err := runWithResponse(t, needsInfo)
		if err != nil {
			t.Fatalf("执行: %v", err)
		}
		if !strings.HasPrefix(got, "NEEDS_INFO|") || strings.Contains(got, `"degraded": true`) {
			t.Fatalf("原生 NEEDS_INFO 不应被标 degraded: %.120s", got)
		}
	})
}
