# T016 教学笔记：Issue Agent——结构化输出、重试与降级

> 对应文件：`app/agents/issue_agent.py`、`tests/agents/test_issue_agent.py`
> 面试视角：这是全项目**最核心的 agent 工程考点**——LLM 输出不可信时的
> 防线设计：结构化约束 → 校验判据 → 重试预算 → 显式降级。

## 1. AgentScope 2.0 的结构化输出 API（1.x 教程全部失效）

```python
agent = Agent(name=..., system_prompt=..., model=DashScopeChatModel(
    credential=DashScopeCredential(api_key=...), model="qwen-plus"))
msg = await agent.reply(inputs=Msg(...), structured_schema=IssueAgentOutput)
data = msg.structured_output   # dict | None —— None 即失败（超轮次/解析失败）
```

写代码前用 `inspect.signature` 核对了 venv 里 2.0.7 的真实签名——**2.0 是推倒重写**，
网上 1.x 教程（`structured_model=`/`api_key=` 参数）全部不存在。查 API 的唯一
可信来源是已安装的包本身，不是搜索引擎。

## 2. 防线四层：从约束到降级

```
第 1 层  structured_schema   约束模型输出格式（AgentScope 内部工具约束）
第 2 层  structured_output 判空  None = 约束被绕过（已知风险 #958）→ 重试
第 3 层  Pydantic model_validate  条件规则校验（ANSWER_READY 必须有引用）→ 重试
第 4 层  重试预算用尽 → 降级 NEEDS_INFO(degraded=true) → 照常入库展示
```

**降级为什么是显式状态而不是抛错？** 抛错会把「模型没配合」上升为「系统故障」，
操作者什么都看不到；降级草稿（ NEEDS_INFO + 3 个通用追问 + degraded 标记）
让操作者知道"系统没把握"——**失败状态也是信息**。Go 侧照常入库，
前端可给 degraded 草稿打上徽标。

已知风险（agentscope#958，未确认修复）：模型可能跳过约束工具直接吐纯文本，
此时 `structured_output` 为 None——所以判空和校验失败都要走重试，
两条路都不能豁免。

## 3. Prompt 工程：引用可核对的两个技巧

1. **文件带行号注入**：
   ```
   1 | pages start at 0
   ```
   模型给出的 `line_start/line_end` 直接对应注入行号——没有行号，"第几行"
   是模型编的；有行号，引用可以被机械核对（AC03 证据链的基础）。
2. **无文件时的先验引导**：prompt 明确"无预取文件 → 优先 NEEDS_INFO/UNRESOLVED"，
   防止模型在零证据时硬编答案。**给模型出口**（让它说"不知道"）比逼它回答
   更能压低幻觉率。

system prompt 的规则顺序也有讲究：结论枚举 → 证据纪律 → 文风 → 格式。
最重要的约束（不许编造引用）放在最前面。

## 4. 降级路径的 usage 也要过 AC47

降级时模型可能根本没被计量（或计量丢失），但 DB 层 `CHECK (cost_cny > 0)`
拒收零费用。`_extract_usage` 的策略：尽力从 `Msg.usage` 提取 → 提取不到
按保守估算下限（输入 1000 + 输出 256 token）→ 再兜底 0.0001 元非零值。
单价未核实（research.md 诚实声明），先 estimated，上线对账后切 confirmed。

## 5. 测试：注入点切在 `reply` 边界

`IssueAgent(reply_fn=...)` 把「调模型」抽象成一个 async callable——测试注入
`SimpleNamespace(structured_output=..., usage=None)` 假 Msg，7 个用例覆盖：
一次成功不重试、None 后重试成功、校验失败后成功、全部失败降级（断言重试
恰好用尽预算 = 3 次调用）、run_id 以请求为准、prompt 行号注入、无文件提示。
**真实模型调用一个都不发**——快、稳、免费，且失败路径（重试/降级）可以精确导演。

## 6. 面试自问自答

- Q: 为什么重试 3 次而不是无限重试？ A: 每次重试都是真金白银的模型调用；
  3 次后成功率边际收益趋零，降级路径的成本（一条追问草稿）远低于继续重试。
- Q: 结构化输出和 function calling 什么关系？ A: AgentScope 2.0 的
  structured_schema 底层用工具约束实现（模型被迫调一个带 schema 的工具）；
  纯文本模式没有这层保证，所以才有 #958 风险和外层 Pydantic 复验。
- Q: 为什么每次请求新建 Agent 实例？ A: M1 单轮任务无状态；AgentScope 2.0
  内置持久化后端是 RedisStorage——引入即违背"不引 Redis"决策（research §5）。
  审计靠 Go 侧持久化请求/响应。

## 7. 自测证据

`pytest tests/`：20 个测试（合同 13 + Agent 7）全绿；无 DASHSCOPE_API_KEY
环境下全量可跑（真模型路径留待 US1 Checkpoint 场景 A）。
