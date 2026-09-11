"""Issue Agent：AgentScope 2.0 结构化输出 + 重试 + 降级（T016）。

设计要点（research.md §5）：
- AgentScope 2.0 大重写：``await agent.reply(inputs, structured_schema=PydanticModel)``，
  结果在 ``Msg.structured_output``（dict）；超轮次/解析失败时为 None（1.x 写法全部失效）。
- 外层 Pydantic 校验是「模型输出不合格 → 重试 → 降级」的判据（T015 的模型在这里消费）。
- 已知风险（agentscope#958）：模型可能跳过约束工具直接输出纯文本——所以
  structured_output is None 与校验失败都必须重试，重试预算用完就降级，
  绝不把未校验文本当草稿交给 Go。
"""
from __future__ import annotations

import asyncio
import os
from typing import Any, Awaitable, Callable

from pydantic import ValidationError

from app.contracts.models import AnalysisRequest, IssueAgentOutput, Outcome, Usage

# 结构化输出重试预算：失败（None/校验不过）→ 重试；用尽 → 降级 NEEDS_INFO。
MAX_ATTEMPTS = 3

SYSTEM_PROMPT = """你是 DevFlow 的 Issue 分析助手。根据用户给出的 Issue 内容与仓库文件，\
产出一份「有依据」的答复或追问。

规则：
1. 结论只能是 ANSWER_READY / NEEDS_INFO / UNRESOLVED：
   - ANSWER_READY：仓库文件足以支撑答案，必须给出 reply_markdown 与 evidence；
   - NEEDS_INFO：信息不足，给出 1-3 个具体追问（needs_info_questions）；
   - UNRESOLVED：无法判断。
2. evidence 的每条引用必须来自「给出的文件内容」，标注 path、sha、行号区间，
   并摘录原文 quote——不得引用文件之外或凭记忆编造的内容。
3. reply_markdown 面向最终读者，简洁、直接引用代码/文档时用代码块。
4. 严格按结构化格式输出，不要输出任何额外文本。"""

USER_TEMPLATE = """## Issue #{number}：{title}

作者：{author}

{body}

## 仓库文件（版本 {head_sha}，只能引用这些内容）

{files}
"""


def build_user_message(req: AnalysisRequest) -> str:
    """把固定快照（Issue + 预取文件）拼成分析输入。

    文件带行号注入：模型据此给出 line_start/line_end，引用可核对（AC03）。
    """
    file_blocks = []
    for f in req.files:
        lines = f.content.splitlines()
        numbered = "\n".join(f"{i:>4} | {line}" for i, line in enumerate(lines, start=1))
        file_blocks.append(f"### {f.path}\n```\n{numbered}\n```")
    files_text = "\n\n".join(file_blocks) if file_blocks else "（无预取文件——优先 NEEDS_INFO 或 UNRESOLVED）"

    return USER_TEMPLATE.format(
        number=req.issue.number,
        title=req.issue.title,
        author=req.issue.author,
        body=req.issue.body or "（无正文）",
        head_sha=req.repo.head_sha,
        files=files_text,
    )


def _extract_usage(msg: Any, model_name: str) -> Usage:
    """从 Msg 尽力提取 token 用量；拿不到就给非零估算下限（AC47：不得记零）。"""
    usage = getattr(msg, "usage", None)
    input_tokens = int(getattr(usage, "input_tokens", 0) or 0)
    output_tokens = int(getattr(usage, "output_tokens", 0) or 0)
    if input_tokens == 0 and output_tokens == 0:
        # 完全无计量时的保守估算：输入按 prompt 下限、输出按 256 估
        input_tokens, output_tokens = 1000, 256
    # qwen-plus 单价未核实（research.md §5），估算值按「千 token 0.002 元」量级计，
    # 上线对账后切 confirmed
    cost = round((input_tokens + output_tokens) * 0.000002, 6) or 0.0001
    return Usage(
        model=model_name,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cost_cny=cost,
        cost_status="estimated",
    )


class IssueAgent:
    """一次请求一个实例（无状态，M1 不用框架持久化——research.md §5）。"""

    def __init__(
        self,
        model_name: str = "qwen-plus",
        max_attempts: int = MAX_ATTEMPTS,
        reply_fn: Callable[[str], Awaitable[Any]] | None = None,
    ) -> None:
        """reply_fn 仅测试注入：模拟 agent.reply(structured_schema=...) 的返回 Msg。

        生产路径按需惰性构造 AgentScope 栈（构造时不要求已配置 API key，
        让「未配 key」在真正调用时才报错——便于无凭据环境跑测试）。
        """
        self._model_name = model_name
        self._max_attempts = max_attempts
        self._reply_fn = reply_fn
        self._agent: Any = None

    def _ensure_agent(self) -> Any:
        if self._agent is not None:
            return self._agent
        from agentscope.agent import Agent
        from agentscope.credential import DashScopeCredential
        from agentscope.message import Msg
        from agentscope.model import DashScopeChatModel

        api_key = os.environ.get("DASHSCOPE_API_KEY")
        if not api_key:
            raise RuntimeError("DASHSCOPE_API_KEY 未配置（参考 infra/.env.example）")
        model = DashScopeChatModel(
            credential=DashScopeCredential(api_key=api_key),
            model=self._model_name,
        )
        self._agent = Agent(
            name="issue-agent",
            system_prompt=SYSTEM_PROMPT,
            model=model,
        )
        self._msg_cls = Msg
        return self._agent

    async def _invoke(self, user_text: str) -> Any:
        """调一次结构化输出，返回原始 Msg（structured_output 可能是 None）。"""
        if self._reply_fn is not None:
            return await self._reply_fn(user_text)
        agent = self._ensure_agent()
        msg = self._msg_cls(name="user", content=user_text, role="user")
        return await agent.reply(inputs=msg, structured_schema=IssueAgentOutput)

    async def analyze(self, req: AnalysisRequest) -> IssueAgentOutput:
        """分析一个 Issue：成功返回合同输出；重试用尽返回降级 NEEDS_INFO。

        降级是显式合同状态（degraded=true + NEEDS_INFO）而不是抛错——
        调用方（Go）把降级草稿照常入库展示，操作者知道系统「没把握」。
        """
        user_text = build_user_message(req)
        last_msg: Any = None

        for _ in range(self._max_attempts):
            msg = await self._invoke(user_text)
            last_msg = msg
            data = getattr(msg, "structured_output", None)
            if data is None:
                continue  # EXCEED_MAX_ITERS / 结构化解析失败：重试
            try:
                out = IssueAgentOutput.model_validate(data)
            except ValidationError:
                continue  # 模型不守规矩：重试
            out.run_id = req.run_id  # 模型可能回显错 run_id，以请求为准
            return out

        # 重试预算用尽：降级（usage 取最后一次调用的计量，尽力而为）
        usage = _extract_usage(last_msg, self._model_name)
        return IssueAgentOutput(
            run_id=req.run_id,
            outcome=Outcome.NEEDS_INFO,
            needs_info_questions=[
                "这个问题发生在什么场景下？",
                "能否补充完整的报错信息或截图？",
                "期望的正确行为是什么？",
            ],
            degraded=True,
            usage=usage,
        )


def run(req: AnalysisRequest) -> IssueAgentOutput:
    """同步入口（FastAPI 端点用 async，这里给脚本/测试一个便捷封装）。"""
    return asyncio.run(IssueAgent().analyze(req))
