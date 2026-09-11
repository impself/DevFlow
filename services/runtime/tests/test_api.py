"""分析端点测试：鉴权、超时、异常→503 的响应语义。

IssueAgent 在端点层经 `_agent()` 工厂构造——测试 monkeypatch 它注入替身，
不发任何真实模型调用。
"""
from __future__ import annotations

import asyncio

import pytest
from fastapi.testclient import TestClient

import app.api as api
from app.contracts.models import IssueAgentOutput, Outcome, Usage


@pytest.fixture()
def client(monkeypatch: pytest.MonkeyPatch) -> TestClient:
    monkeypatch.setenv("DEVFLOW_INTERNAL_TOKEN", "test-token")
    # 收集器缓存了旧 import，确保每次拿到新配置
    import importlib

    importlib.reload(api)
    from app.main import app

    return TestClient(app)


def valid_output() -> IssueAgentOutput:
    return IssueAgentOutput(
        run_id="run-1",
        outcome=Outcome.ANSWER_READY,
        reply_markdown="答案。",
        evidence=[{
            "source_type": "doc_file",
            "location": {"path": "README.md", "sha": "3f2a1bc", "line_start": 1, "line_end": 1},
            "quote": "quote",
        }],
        usage=Usage(model="qwen-plus", input_tokens=10, output_tokens=5,
                    cost_cny=0.01, cost_status="estimated"),
    )


def make_request_body() -> dict:
    return {
        "run_id": "run-1",
        "issue": {"number": 7, "title": "t", "body": "b", "author": "a"},
        "repo": {"numeric_id": 42, "full_name": "o/n", "head_sha": "fixed0sha"},
        "files": [{"path": "README.md", "content": "hello"}],
    }


def patch_agent(monkeypatch: pytest.MonkeyPatch, behavior) -> None:
    class FakeAgent:
        async def analyze(self, req) -> IssueAgentOutput:
            return await behavior(req)

    monkeypatch.setattr(api, "_agent", lambda: FakeAgent())


class TestAuth:
    def test_missing_token_401(self, client: TestClient) -> None:
        assert client.post("/internal/v1/issue-analysis", json=make_request_body()).status_code == 401

    def test_wrong_token_401(self, client: TestClient) -> None:
        r = client.post("/internal/v1/issue-analysis", json=make_request_body(),
                        headers={"X-DevFlow-Internal-Token": "wrong"})
        assert r.status_code == 401

    def test_unconfigured_token_401_fail_fast(
        self, monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        monkeypatch.delenv("DEVFLOW_INTERNAL_TOKEN", raising=False)
        import importlib
        importlib.reload(api)
        from app.main import app
        c = TestClient(app)
        r = c.post("/internal/v1/issue-analysis", json=make_request_body(),
                   headers={"X-DevFlow-Internal-Token": "anything"})
        assert r.status_code == 401


class TestEndpoint:
    def test_success_200_and_no_null_fields(self, client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
        async def ok(req) -> IssueAgentOutput:
            out = valid_output()
            out.run_id = req.run_id
            return out

        patch_agent(monkeypatch, ok)
        r = client.post("/internal/v1/issue-analysis", json=make_request_body(),
                        headers={"X-DevFlow-Internal-Token": "test-token"})
        assert r.status_code == 200
        body = r.json()
        assert body["outcome"] == "ANSWER_READY"
        assert "needs_info_questions" not in body  # None 必须缺省（T015 教训）

    def test_invalid_body_422(self, client: TestClient) -> None:
        bad = make_request_body() | {"issue": {"number": 7}}  # 缺 title/body/author
        r = client.post("/internal/v1/issue-analysis", json=bad,
                        headers={"X-DevFlow-Internal-Token": "test-token"})
        assert r.status_code == 422

    def test_internal_error_503(self, client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
        async def boom(req):
            raise RuntimeError("DASHSCOPE_API_KEY 未配置")

        patch_agent(monkeypatch, boom)
        r = client.post("/internal/v1/issue-analysis", json=make_request_body(),
                        headers={"X-DevFlow-Internal-Token": "test-token"})
        assert r.status_code == 503

    def test_timeout_503(self, client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setattr(api, "ANALYSIS_TIMEOUT_SECONDS", 0.05)

        async def slow(req):
            await asyncio.sleep(1)
            return valid_output()

        patch_agent(monkeypatch, slow)
        r = client.post("/internal/v1/issue-analysis", json=make_request_body(),
                        headers={"X-DevFlow-Internal-Token": "test-token"})
        assert r.status_code == 503
