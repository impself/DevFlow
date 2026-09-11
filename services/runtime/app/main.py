# DevFlow 智能层入口：FastAPI 应用装配。
# 职责边界（plan.md）：runtime 只做「固定输入 → 结构化分析结果」的无状态服务，
# 不持数据库凭据、不直接写 GitHub（宪法 III/IV）——那是 Go 控制层的事。
from __future__ import annotations

import logging
import os

from fastapi import FastAPI

from app.api import router

# 兜底日志配置：uvicorn 默认不接管业务 logger，
# 没有 handler 时 INFO 级日志会被静默吞掉（Python 兜底只放行 WARNING+）。
logging.basicConfig(
    level=os.environ.get("LOG_LEVEL", "INFO").upper(),
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
logger = logging.getLogger("devflow.runtime")

app = FastAPI(
    title="DevFlow Runtime",
    version="0.1.0",
    description="Issue 分析智能层（AgentScope 2.0），仅接受控制层内部调用",
)
app.include_router(router)


@app.get("/healthz")
def healthz() -> dict:
    """健康检查与依赖状态。

    runtime 没有数据库；它能否干活取决于 DashScope 凭据是否配置。
    这里只汇报「配置了没有」（布尔值），绝不回显密钥内容。
    缺凭据时返回 degraded：进程活着，但模型调用必然失败。
    """
    dashscope_ready = bool(os.environ.get("DASHSCOPE_API_KEY"))
    status = "ok" if dashscope_ready else "degraded"
    return {
        "status": status,
        "service": "devflow-runtime",
        "dashscope_configured": dashscope_ready,
    }
