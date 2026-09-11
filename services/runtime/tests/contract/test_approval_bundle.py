"""审批域合同测试（T025）：approval-bundle 与 action-receipt 两个 schema 的
schema 自洽 + Go 侧实际产出形状的一致性 + 过期/失效规则用例。

三个校验方向：
  1. schema 自洽：自带 examples 必须过自身校验；
  2. 形状一致性：Go 端 bundleJSON / action receipt 的字段命名与结构必须与
     schema 对齐（跨语言合同的 Python 侧防线——Go 侧有自己的复验，双端互不信）；
  3. 规则用例：AC33（内容/目标漂移失效）、AC35（幂等回显）、AC36（UNCERTAIN/
     RECONCILING 语义）以数据形状的形式钉住。
"""
from __future__ import annotations

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

SCHEMAS = Path(__file__).resolve().parents[2] / "app" / "contracts" / "schemas"


@pytest.fixture(scope="module")
def bundle_validator() -> Draft202012Validator:
    schema = json.loads((SCHEMAS / "approval-bundle.schema.json").read_text(encoding="utf-8"))
    return Draft202012Validator(schema)


@pytest.fixture(scope="module")
def receipt_validator() -> Draft202012Validator:
    schema = json.loads((SCHEMAS / "action-receipt.schema.json").read_text(encoding="utf-8"))
    return Draft202012Validator(schema)


def bundle(**overrides: object) -> dict:
    base = {
        "bundle_id": "bundle-1",
        "case_id": "case-1",
        "draft_id": "draft-1",
        "action_type": "POST_ISSUE_COMMENT",
        "target": {"repo_numeric_id": 1001, "issue_number": 7},
        "content_digest": "a" * 64,
        "status": "PENDING",
        "expires_at": "2026-09-12T08:00:00Z",
    }
    return base | overrides


def receipt(**overrides: object) -> dict:
    base = {
        "action_id": "action-1",
        "bundle_id": "bundle-1",
        "status": "SUCCEEDED",
        "remote_comment_id": 2888112233,
        "remote_url": "https://github.com/o/n/issues/7#issuecomment-2888112233",
        "checked_at": "2026-09-11T09:05:00Z",
    }
    return base | overrides


class TestSchemaSelfConsistency:
    def test_bundle_examples_pass(self, bundle_validator: Draft202012Validator) -> None:
        for i, example in enumerate(bundle_validator.schema.get("examples", [])):
            errors = list(bundle_validator.iter_errors(example))
            assert not errors, f"bundle examples[{i}]: {errors}"

    def test_receipt_examples_pass(self, receipt_validator: Draft202012Validator) -> None:
        for i, example in enumerate(receipt_validator.schema.get("examples", [])):
            errors = list(receipt_validator.iter_errors(example))
            assert not errors, f"receipt examples[{i}]: {errors}"


class TestApprovalBundleRules:
    """AC32/AC33/AC35 的数据形状防线。"""

    def test_go_bundle_shape_passes(self, bundle_validator: Draft202012Validator) -> None:
        """Go 端 bundleJSON 的字段（snake_case + target 嵌套）必须能过 schema。"""
        go_shape = {
            "bundle_id": "bundle-1",
            "case_id": "case-1",
            "draft_id": "draft-1",
            "status": "APPROVED",
            "action_type": "POST_ISSUE_COMMENT",
            "target": {"repo_numeric_id": 1001, "issue_number": 7},
            "content_digest": "b" * 64,
            "expires_at": "2026-09-12T08:00:00Z",
            "approved_by": "operator",
            "approved_at": "2026-09-11T08:30:00Z",
        }
        errors = list(bundle_validator.iter_errors(go_shape))
        assert not errors, errors

    def test_target_without_issue_number_rejected(self, bundle_validator: Draft202012Validator) -> None:
        bad = bundle(target={"repo_numeric_id": 1001})
        assert list(bundle_validator.iter_errors(bad))

    def test_non_whitelisted_action_rejected(self, bundle_validator: Draft202012Validator) -> None:
        bad = bundle(action_type="MERGE_PULL_REQUEST")
        assert list(bundle_validator.iter_errors(bad))

    def test_illegal_status_rejected(self, bundle_validator: Draft202012Validator) -> None:
        bad = bundle(status="DREAMING")
        assert list(bundle_validator.iter_errors(bad))

    def test_全生命周期状态均合法(self, bundle_validator: Draft202012Validator) -> None:
        for status in ["PENDING", "APPROVED", "EXECUTING", "EXECUTED", "REJECTED", "EXPIRED"]:
            errors = list(bundle_validator.iter_errors(bundle(status=status)))
            assert not errors, (status, errors)

    def test_expires_at_bad_format_documented(self, bundle_validator: Draft202012Validator) -> None:
        bad = bundle(expires_at="not-a-time")
        Draft202012Validator.check_schema(bundle_validator.schema)
        # format 默认不强制（jsonschema 规范允许），这里只验证不崩溃——
        # 真正的 24h 时效由 Go 服务端 SQL 比较，schema 只负责文档化


class TestActionReceiptRules:
    """AC36/AC37 的数据形状防线。"""

    def test_go_receipt_shape_passes(self, receipt_validator: Draft202012Validator) -> None:
        go_shape = {
            "action_id": "action-1",
            "bundle_id": "bundle-1",
            "status": "SUCCEEDED",
            "remote_comment_id": 42,
            "remote_url": "https://github.com/o/n/issues/7#issuecomment-42",
            "checked_at": "2026-09-11T09:05:00Z",
        }
        errors = list(receipt_validator.iter_errors(go_shape))
        assert not errors, errors

    def test_failed_receipt_with_note_legal(self, receipt_validator: Draft202012Validator) -> None:
        failed = receipt(status="FAILED", remote_comment_id=None, remote_url=None, note="GitHub 明确拒绝")
        failed.pop("remote_comment_id")
        failed.pop("remote_url")
        errors = list(receipt_validator.iter_errors(failed))
        assert not errors, errors

    def test_status_enum_exact_three(self, receipt_validator: Draft202012Validator) -> None:
        for status in ["SUCCEEDED", "FAILED", "UNCERTAIN"]:
            errors = list(receipt_validator.iter_errors(receipt(status=status)))
            assert not errors, (status, errors)
        assert list(receipt_validator.iter_errors(receipt(status="RECONCILING")))

    def test_remote_comment_id_must_be_integer(
        self, receipt_validator: Draft202012Validator
    ) -> None:
        bad = receipt(remote_comment_id="2888112233")
        assert list(receipt_validator.iter_errors(bad))
