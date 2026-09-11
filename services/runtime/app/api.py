"""内部分析端点（T017）：Go Runner → 智能层的唯一入口。

响应语义为调用方（Go Runner 的重试路径）设计：
- 401：内部令牌缺失/错误（不该重试，是配置错误）；
- 422：请求体不满足合同（Pydantic 自动校验，FastAPI 默认行为）；
- 503：智能层过载/超时/内部错误——**明确告诉调用方"这是临时态，值得重试"**。
"""
from __future__ import annotations

import asyncio
import logging
import os
import secrets

from fastapi import APIRouter, Depends, Header, HTTPException

from app.agents.issue_agent import IssueAgent
from app.contracts.models import AnalysisRequest, IssueAgentOutput

logger = logging.getLogger("devflow.runtime.api")

# 单次分析的超时上限：必须小于 Go 侧的 300s 兜底（execute.go analyzeTimeout），
# 让超时先在 Python 侧发生——503 语义由这里给，而不是让 Go 侧超时猜。
ANALYSIS_TIMEOUT_SECONDS = 240.0

router = APIRouter()


def _agent() -> IssueAgent:
    """每次请求新建实例（无状态；测试在这里 monkeypatch 替身）。"""
    return IssueAgent()


async def require_internal_token(
    x_devflow_internal_token: str = Header(
        alias="X-DevFlow-Internal-Token", default="",
    ),
) -> None:
    """服务间鉴权（宪法 III）：共享密钥 + constant-time 比较。

    密钥未配置直接 401——和 Go 侧 ParseDelivery 拒绝空 secret 同一哲学：
    误配置宁可当场失败，不可静默放行。
    """
    expected = os.environ.get("DEVFLOW_INTERNAL_TOKEN", "")
    if not expected or not secrets.compare_digest(x_devflow_internal_token, expected):
        raise HTTPException(status_code=401, detail="缺少或错误的内部令牌")


@router.post(
    "/internal/v1/issue-analysis",
    response_model=IssueAgentOutput,
    dependencies=[Depends(require_internal_token)],
)
async def issue_analysis(req: AnalysisRequest) -> IssueAgentOutput:
    """同步分析一个 Issue：成功返回合同输出；任何内部失败一律 503。

    为什么异常统一 503 而不是 500？503 语义 = "服务暂不可用，稍后重试"，
    Go Runner 收到后走重试路径（领取重试/新 run）；500 语义 = "请求有错"，
    重试无意义。分析失败几乎总是临时态（模型超时/过载/网络抖动）。
    """
    try:
        async with asyncio.timeout(ANALYSIS_TIMEOUT_SECONDS):
            return await _agent().analyze(req)
    except TimeoutError:
        logger.warning("分析超时（%ss），run=%s", ANALYSIS_TIMEOUT_SECONDS, req.run_id)
        raise HTTPException(status_code=503, detail="分析超时，请稍后重试") from None
    except RuntimeError as e:
        # 典型：DASHSCOPE_API_KEY 未配置——属部署问题，操作者看日志修配置
        logger.error("智能层未就绪: %s", e)
        raise HTTPException(status_code=503, detail="智能层未就绪") from None
    except Exception as e:  # noqa: BLE001 —— 边界层兜底，错误详情只进日志
        logger.exception("分析失败 run=%s: %s", req.run_id, e)
        raise HTTPException(status_code=503, detail="分析失败，请稍后重试") from None
