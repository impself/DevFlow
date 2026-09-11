"""contracts 包：contracts/schemas/*.json 的 Pydantic 镜像模型。

铁律（PRD §28.2）：schema 是唯一真源，镜像必须跟随——schema 改了先改这里。
条件规则（ANSWER_READY 必须有 reply+evidence 等）用 model_validator 实现，
与 schema 的 allOf/if-then 一一对应，注释里互引。
"""
from __future__ import annotations

from enum import Enum
from typing import Literal

from pydantic import BaseModel, Field, model_validator


class Outcome(str, Enum):
    """Run 结论（PRD §11.1 M1 子集）。"""

    ANSWER_READY = "ANSWER_READY"
    NEEDS_INFO = "NEEDS_INFO"
    UNRESOLVED = "UNRESOLVED"


class Location(BaseModel):
    """证据位置：path + blob SHA（AC03 要求可核对到固定版本）。"""

    path: str
    sha: str = Field(pattern=r"^[0-9a-f]{7,40}$")
    line_start: int | None = Field(default=None, ge=1)
    line_end: int | None = Field(default=None, ge=1)


class Evidence(BaseModel):
    """一条证据引用：来源类型 + 位置 + 原文摘录。"""

    source_type: Literal["doc_file", "code_file"]
    location: Location
    quote: str | None = None


class Usage(BaseModel):
    """模型调用计量（FR-10/AC47）：cost 不得记零的防线在 Go 存储层 CHECK。"""

    model: str
    input_tokens: int = Field(ge=0)
    output_tokens: int = Field(ge=0)
    cost_cny: float = Field(ge=0)
    cost_status: Literal["estimated", "confirmed"]


class IssueAgentOutput(BaseModel):
    """issue-agent-output.schema.json 的 Pydantic 镜像（智能层的出口合同）。

    内部先经本模型校验，Go 侧收到后还会按 schema 原文复验——
    双向校验是跨语言合同的标准姿势。
    """

    run_id: str
    outcome: Outcome
    reply_markdown: str | None = None
    evidence: list[Evidence] = Field(default_factory=list)
    needs_info_questions: list[str] | None = None
    degraded: bool = False
    usage: Usage

    def model_dump_json(self, **kwargs: object) -> str:
        """可选字段（reply/evidence/questions）为 None 时必须「缺省」而不是 null——
        schema 未定义 nullable，null 会过不了 Go 侧复验。
        统一在序列化出口排除 None，调用方无需记住 exclude_none。"""
        kwargs.setdefault("exclude_none", True)
        return super().model_dump_json(**kwargs)  # type: ignore[return-value]

    @model_validator(mode="after")
    def _check_conditional_rules(self) -> "IssueAgentOutput":
        """镜像 schema 的三条 allOf/if-then 条件规则（改动必须同步 schema）。"""
        if self.outcome is Outcome.ANSWER_READY:
            if not self.reply_markdown or not self.evidence:
                raise ValueError("ANSWER_READY 必须携带 reply_markdown 与 evidence（≥1 条）")
            if self.degraded:
                raise ValueError("degraded=true 时 outcome 不得为 ANSWER_READY")
        if self.outcome is Outcome.NEEDS_INFO:
            if not self.needs_info_questions:
                raise ValueError("NEEDS_INFO 必须给出 needs_info_questions（1-3 条）")
            if len(self.needs_info_questions) > 3:
                raise ValueError("追问至多 3 条（AC04）")
        return self


# ===== 入站模型（openapi.yaml /internal/v1/issue-analysis 请求体镜像，T017 使用） =====


class IssueInput(BaseModel):
    number: int
    title: str
    body: str
    author: str


class RepoInput(BaseModel):
    numeric_id: int
    full_name: str
    head_sha: str


class File(BaseModel):
    path: str
    content: str


class AnalysisRequest(BaseModel):
    """Go Runner → 智能层的分析请求（固定快照，FR-3）。"""

    run_id: str
    issue: IssueInput
    repo: RepoInput
    files: list[File] = Field(default_factory=list)
