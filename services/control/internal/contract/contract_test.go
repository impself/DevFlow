package contract_test

import (
	"testing"

	"github.com/impself/DevFlow/services/control/internal/contract"
)

// 用 schema 自带的 example 的变体做合同校验的回归测试。
// 条件规则（allOf/if-then）是最容易漂移的地方，逐条钉住。

const validAnswerReady = `{
  "run_id": "run-1", "outcome": "ANSWER_READY",
  "reply_markdown": "见 README。",
  "evidence": [{"source_type":"doc_file","location":{"path":"README.md","sha":"3f2a1bc","line_start":1,"line_end":2},"quote":"hello"}],
  "degraded": false,
  "usage": {"model":"qwen-plus","input_tokens":100,"output_tokens":10,"cost_cny":0.01,"cost_status":"estimated"}
}`

func TestValidateIssueAgentOutput(t *testing.T) {
	t.Run("合法 ANSWER_READY 通过", func(t *testing.T) {
		out, err := contract.ValidateIssueAgentOutput([]byte(validAnswerReady))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if out.Outcome != "ANSWER_READY" || out.Usage.Model != "qwen-plus" {
			t.Fatalf("字段解析不符: %+v", out)
		}
	})

	t.Run("ANSWER_READY 缺 reply_markdown 拒绝", func(t *testing.T) {
		bad := `{"run_id":"r","outcome":"ANSWER_READY","evidence":[{"source_type":"doc_file","location":{"path":"a.md","sha":"abc1234"}}],"usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0.1,"cost_status":"estimated"}}`
		if _, err := contract.ValidateIssueAgentOutput([]byte(bad)); err == nil {
			t.Fatal("缺 reply_markdown 应被 schema 拒绝")
		}
	})

	t.Run("degraded=true 不得 ANSWER_READY", func(t *testing.T) {
		bad := `{"run_id":"r","outcome":"ANSWER_READY","reply_markdown":"x","evidence":[{"source_type":"doc_file","location":{"path":"a.md","sha":"abc1234"}}],"degraded":true,"usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0.1,"cost_status":"estimated"}}`
		if _, err := contract.ValidateIssueAgentOutput([]byte(bad)); err == nil {
			t.Fatal("degraded=true 时 ANSWER_READY 应被 schema 拒绝")
		}
	})

	t.Run("NEEDS_INFO 缺 questions 拒绝", func(t *testing.T) {
		bad := `{"run_id":"r","outcome":"NEEDS_INFO","usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0.1,"cost_status":"estimated"}}`
		if _, err := contract.ValidateIssueAgentOutput([]byte(bad)); err == nil {
			t.Fatal("NEEDS_INFO 缺 needs_info_questions 应被拒绝")
		}
	})

	t.Run("追问超过 3 条拒绝（AC04）", func(t *testing.T) {
		bad := `{"run_id":"r","outcome":"NEEDS_INFO","needs_info_questions":["1","2","3","4"],"usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0.1,"cost_status":"estimated"}}`
		if _, err := contract.ValidateIssueAgentOutput([]byte(bad)); err == nil {
			t.Fatal("4 条追问应被 maxItems 拒绝")
		}
	})

	t.Run("cost_cny=0 在合同层合法——防线在存储层（层间职责）", func(t *testing.T) {
		// schema 是 minimum: 0（合同作者的选择）；「不得记零」(AC47) 由
		// model_calls 表的 CHECK (cost_cny > 0) 强制（见 T006 迁移测试）。
		// 合同校验与存储约束各司其职，不要在 Go 里重复实现 schema 的规则。
		bad := `{"run_id":"r","outcome":"UNRESOLVED","usage":{"model":"m","input_tokens":1,"output_tokens":1,"cost_cny":0,"cost_status":"estimated"}}`
		if _, err := contract.ValidateIssueAgentOutput([]byte(bad)); err != nil {
			t.Fatalf("合同层应放行 cost_cny=0（存储层负责拒绝）: %v", err)
		}
	})
}
