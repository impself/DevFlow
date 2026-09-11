"""issue-agent-output 合同测试：Pydantic 镜像与 JSON Schema 双向校验。

双向的含义：
  1. schema → 校验合法示例通过、条件违规被拒（schema 本身的回归防线）；
  2. Pydantic 实例 → 序列化后的 JSON 也必须过 schema（镜像没有漂移）。
镜像漂移（改了模型忘了改 schema，或反之）在任何一侧都会立刻炸测试。
"""
from __future__ import annotations

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator
from pydantic import ValidationError

from app.contracts.models import IssueAgentOutput, Outcome, Usage

SCHEMA_PATH = (
    Path(__file__).resolve().parents[2]
    / "app" / "contracts" / "schemas" / "issue-agent-output.schema.json"
)


@pytest.fixture(scope="module")
def schema() -> dict:
    return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))


@pytest.fixture(scope="module")
def validator(schema: dict) -> Draft202012Validator:
    return Draft202012Validator(schema)


def usage() -> dict:
    return {
        "model": "qwen-plus",
        "input_tokens": 100,
        "output_tokens": 10,
        "cost_cny": 0.01,
        "cost_status": "estimated",
    }


def answer_ready_output() -> dict:
    return {
        "run_id": "run-1",
        "outcome": "ANSWER_READY",
        "reply_markdown": "分页从 0 开始，见 README 第 3 行。",
        "evidence": [
            {
                "source_type": "doc_file",
                "location": {"path": "README.md", "sha": "3f2a1bc", "line_start": 3, "line_end": 3},
                "quote": "pages start at 0",
            }
        ],
        "degraded": False,
        "usage": usage(),
    }


def needs_info_output() -> dict:
    return {
        "run_id": "run-2",
        "outcome": "NEEDS_INFO",
        "needs_info_questions": ["使用的版本号是？", "完整报错输出是？"],
        "usage": usage(),
    }


class TestSchemaSelf:
    def test_schema_examples_pass_own_validation(self, validator: Draft202012Validator) -> None:
        for i, example in enumerate(validator.schema.get("examples", [])):
            errors = list(validator.iter_errors(example))
            assert not errors, f"schema examples[{i}] 违反自身: {errors}"

    def test_answer_ready_passes_schema(self, validator: Draft202012Validator) -> None:
        errors = list(validator.iter_errors(answer_ready_output()))
        assert not errors, errors

    def test_needs_info_passes_schema(self, validator: Draft202012Validator) -> None:
        errors = list(validator.iter_errors(needs_info_output()))
        assert not errors, errors


class TestPydanticMirror:
    def test_pydantic_serialization_passes_schema(self, validator: Draft202012Validator) -> None:
        """镜像不漂移的核心断言：模型吐出的 JSON 就是 schema 认可的 JSON。"""
        out = IssueAgentOutput.model_validate(answer_ready_output())
        errors = list(validator.iter_errors(json.loads(out.model_dump_json())))
        assert not errors, errors

    def test_schema_examples_parse_into_pydantic(self) -> None:
        validator = Draft202012Validator(json.loads(SCHEMA_PATH.read_text(encoding="utf-8")))
        for example in validator.schema.get("examples", []):
            out = IssueAgentOutput.model_validate(example)
            assert out.usage.model

    def test_round_trip(self, validator: Draft202012Validator) -> None:
        out = IssueAgentOutput.model_validate(needs_info_output())
        again = IssueAgentOutput.model_validate_json(out.model_dump_json())
        assert again == out


class TestConditionalRules:
    """三条条件规则（对应 schema 的 allOf/if-then，改动必须同步）。"""

    def test_answer_ready_without_reply_rejected(self) -> None:
        bad = answer_ready_output() | {"reply_markdown": None}
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)

    def test_answer_ready_without_evidence_rejected(self) -> None:
        bad = answer_ready_output() | {"evidence": []}
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)

    def test_degraded_with_answer_ready_rejected(self) -> None:
        bad = answer_ready_output() | {"degraded": True}
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)

    def test_needs_info_without_questions_rejected(self) -> None:
        bad = needs_info_output() | {"needs_info_questions": None}
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)

    def test_more_than_three_questions_rejected(self) -> None:
        bad = needs_info_output() | {"needs_info_questions": ["1", "2", "3", "4"]}
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)

    def test_degraded_needs_info_is_legal(self) -> None:
        good = needs_info_output() | {"degraded": True}
        out = IssueAgentOutput.model_validate(good)
        assert out.outcome is Outcome.NEEDS_INFO and out.degraded

    def test_sha_pattern_violation_rejected(self) -> None:
        bad = answer_ready_output()
        bad["evidence"][0]["location"]["sha"] = "NOT-HEX"
        with pytest.raises(ValidationError):
            IssueAgentOutput.model_validate(bad)
