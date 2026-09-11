// Package contract 是跨语言合同的 Go 侧锚点：
// 内嵌 JSON Schema 原文并提供校验，Go struct 只是镜像。
// 铁律（PRD §28.2 / tasks.md Notes）：contracts/ 的 schema 是唯一真源，
// 本包的 struct/校验必须跟随 schema 变化——schema 改了先改这里。
package contract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/issue-agent-output.schema.json
var issueAgentOutputSchemaJSON []byte

var issueAgentOutputSchema *jsonschema.Schema

func init() {
	// v6 API：UnmarshalJSON 吃 io.Reader；Compile 前需 AddResource 注册文档
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(issueAgentOutputSchemaJSON))
	if err != nil {
		panic(fmt.Sprintf("contract: 解析 issue-agent-output schema: %v", err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("issue-agent-output.schema.json", doc); err != nil {
		panic(fmt.Sprintf("contract: 注册 schema: %v", err))
	}
	issueAgentOutputSchema, err = compiler.Compile("issue-agent-output.schema.json")
	if err != nil {
		panic(fmt.Sprintf("contract: 编译 issue-agent-output schema: %v", err))
	}
}

// ValidateIssueAgentOutput 按 schema 校验原始 JSON（Go 侧复验，合同优先）。
// 返回解析后的镜像 struct；校验失败返回包含具体违规路径的错误。
func ValidateIssueAgentOutput(raw []byte) (AgentOutput, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return AgentOutput{}, fmt.Errorf("issue-agent-output 不是合法 JSON: %w", err)
	}
	if err := issueAgentOutputSchema.Validate(v); err != nil {
		return AgentOutput{}, fmt.Errorf("issue-agent-output 违反合同: %w", err)
	}
	var out AgentOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return AgentOutput{}, fmt.Errorf("issue-agent-output 反序列化: %w", err)
	}
	return out, nil
}

// AgentOutput 是 issue-agent-output.schema.json 的 Go 镜像（只读视角）。
// 条件规则（ANSWER_READY 必须有 reply+evidence 等）由 schema 校验兜底，
// struct 里不重复实现——单一事实来源。
type AgentOutput struct {
	RunID              string     `json:"run_id"`
	Outcome            string     `json:"outcome"` // ANSWER_READY | NEEDS_INFO | UNRESOLVED
	ReplyMarkdown      string     `json:"reply_markdown,omitempty"`
	Evidence           []Evidence `json:"evidence,omitempty"`
	NeedsInfoQuestions []string   `json:"needs_info_questions,omitempty"`
	Degraded           bool       `json:"degraded,omitempty"`
	Usage              Usage      `json:"usage"`
}

type Evidence struct {
	SourceType string   `json:"source_type"` // doc_file | code_file
	Location   Location `json:"location"`
	Quote      string   `json:"quote,omitempty"`
}

type Location struct {
	Path      string `json:"path"`
	SHA       string `json:"sha"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

type Usage struct {
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostCNY      float64 `json:"cost_cny"`
	CostStatus   string  `json:"cost_status"` // estimated | confirmed
}
