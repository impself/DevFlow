"""IssueAgent 重试/降级逻辑测试（不依赖真实模型——reply_fn 注入假 Msg）。

三种核心路径：
  1. 一次成功：structured_output 合法 → 直接返回（不重试）；
  2. 结构化输出 None（EXCEED_MAX_ITERS）/ 校验失败 → 重试 → 第 N 次成功；
  3. 全部失败 → 降级 NEEDS_INFO（degraded=true）。
"""
from __future__ import annotations

from types import SimpleNamespace

import pytest

from app.agents.issue_agent import IssueAgent, build_user_message
from app.contracts.models import AnalysisRequest, IssueInput, Outcome, File, RepoInput, Usage


def make_request() -> AnalysisRequest:
    return AnalysisRequest(
        run_id="run-xyz",
        issue=IssueInput(number=7, title="分页参数", body="page 从几开始？", author="alice"),
        repo=RepoInput(numeric_id=42, full_name="o/n", head_sha="fixed0sha"),
        files=[File(path="README.md", content="pages start at 0")],
    )


def valid_output(run_id: str = "run-xyz") -> dict:
    return {
        "run_id": run_id,
        "outcome": "ANSWER_READY",
        "reply_markdown": "从 0 开始，见 README。",
        "evidence": [
            {"source_type": "doc_file",
             "location": {"path": "README.md", "sha": "3f2a1bc", "line_start": 1, "line_end": 1},
             "quote": "pages start at 0"}
        ],
        "usage": {"model": "qwen-plus", "input_tokens": 100, "output_tokens": 10,
                  "cost_cny": 0.01, "cost_status": "estimated"},
    }


def fake_msg(structured_output: dict | None) -> SimpleNamespace:
    return SimpleNamespace(structured_output=structured_output, usage=None)


class TestAnalyze:
    async def test_first_try_success_no_retry(self) -> None:
        calls = []

        async def reply(text: str) -> SimpleNamespace:
            calls.append(text)
            return fake_msg(valid_output())

        agent = IssueAgent(reply_fn=reply, max_attempts=3)
        out = await agent.analyze(make_request())
        assert out.outcome is Outcome.ANSWER_READY and not out.degraded
        assert len(calls) == 1

    async def test_none_then_retry_success(self) -> None:
        seq = [fake_msg(None), fake_msg(valid_output())]
        calls = []

        async def reply(text: str) -> SimpleNamespace:
            calls.append(text)
            return seq.pop(0)

        out = await IssueAgent(reply_fn=reply, max_attempts=3).analyze(make_request())
        assert out.outcome is Outcome.ANSWER_READY
        assert len(calls) == 2

    async def test_invalid_then_success(self) -> None:
        bad = valid_output() | {"reply_markdown": None}  # ANSWER_READY 缺 reply
        seq = [fake_msg(bad), fake_msg(valid_output())]

        async def reply(text: str) -> SimpleNamespace:
            return seq.pop(0)

        out = await IssueAgent(reply_fn=reply, max_attempts=3).analyze(make_request())
        assert out.outcome is Outcome.ANSWER_READY

    async def test_all_failed_degrades_to_needs_info(self) -> None:
        calls = []

        async def reply(text: str) -> SimpleNamespace:
            calls.append(text)
            return fake_msg(None)  # 模拟 EXCEED_MAX_ITERS

        out = await IssueAgent(reply_fn=reply, max_attempts=3).analyze(make_request())
        assert out.outcome is Outcome.NEEDS_INFO
        assert out.degraded is True
        assert 1 <= len(out.needs_info_questions) <= 3
        assert len(calls) == 3  # 重试预算用尽
        # AC47：降级路径的 usage 也不得记零
        assert out.usage.cost_cny > 0

    async def test_run_id_from_request_wins(self) -> None:
        async def reply(text: str) -> SimpleNamespace:
            return fake_msg(valid_output(run_id="模型回显的错误id"))

        out = await IssueAgent(reply_fn=reply, max_attempts=1).analyze(make_request())
        assert out.run_id == "run-xyz"


class TestPrompt:
    def test_files_injected_with_line_numbers(self) -> None:
        text = build_user_message(make_request())
        assert "README.md" in text
        assert "1 | pages start at 0" in text
        assert "fixed0sha" in text

    def test_no_files_hints_needs_info(self) -> None:
        req = make_request()
        req.files = []
        text = build_user_message(req)
        assert "无预取文件" in text
