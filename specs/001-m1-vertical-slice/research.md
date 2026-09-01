# Phase 0 Research: M1 主线竖切

**Date**: 2026-09-01 | **方法**: 三个并行调研（同类项目 / Go 基础设施 / AgentScope），
GitHub 元数据（license、star、最近推送）经 GitHub API 或 LICENSE 原文核实。
**诚实声明**：凡标注「未核实」的事实不得在文档与简历中当作已确认引用（宪法原则 V/VIII）。

## 1. 同类项目对比（Issue/PR 自动处理 Agent）

| 项目 | License | 状态 | Decision |
| --- | --- | --- | --- |
| The-PR-Agent/pr-agent（Qodo 开源版） | MIT（当前 LICENSE 原文核实；历史变迁未核实） | 活跃（12.8k★，2026-08 推送），已移交社区 | **借鉴模式，不复用代码** |
| anthropics/claude-code-action（+ base-action） | MIT | 活跃（8.8k★） | **借鉴「生成/发布分离」模式** |
| sweepai/sweep | 自定义许可（NOASSERTION，禁止分发/衍生） | 实质停更（2025-09 后无推送），转向 JetBrains 产品 | **不采用（法律上不可复用），作反面教材** |
| OpenHands、SWE-agent | 均 MIT | 活跃（85.8k★ / 20.2k★） | **不采用**：重量级通用编码 Agent，威胁模型（沙箱执行、补丁）与 M1「只发一条审批过的评论」不匹配 |
| probot/probot | ISC | 活跃 | **借鉴 webhook 事件路由抽象**（Node 生态，代码不复用） |
| google/triage-party | Apache-2.0 | 低频维护 | **不采用**：纯规则分诊、产出看板，与 M1 目标不同 |

**借鉴清单（直接抄的作业）**：
1. Webhook 快速 ACK + 异步处理（所有同类项目的共同结构）
2. 生成与发布分离，在分离点插入审批（claude-code-base-action 验证了拆分点成立）
3. 评论发布幂等：以业务键（issue + 批准批次）控制，重试/重放不重复发布
4. 触发门控，防止 bot 互喷（M1 只订阅 issues opened/reopened + 自评论去重）
5. 凭据教训：pr-agent `/help_docs` 因抓取外部文档泄密被下架（issue #2445）——M1 的引用只允许来自固定 SHA 的仓库内容，不抓取任意外链

**必须自研（无先例可抄）**：跨语言租约协议、审批绑定与失效、带引用的 Issue 答复生成、
webhook 多仓库入口与 secret 管理。
**反向印证**：Sweep 证明全自动 issue→PR 难以稳定交付；DevFlow 的 human-in-the-loop 路线
（M1 只出草稿 + 审批发布）与被验证更可靠的方向一致。

## 2. Go 任务队列与租约协议

**Decision: 不引入任何队列库，自研 runs 表租约协议（pgx + `FOR UPDATE SKIP LOCKED`）。**

- **River**（riverqueue/river，MPL-2.0，Pro 版付费，v0.47、极活跃）：事务性入队模型干净，但开源核心无 per-job 租约/心跳/epoch 概念；worker 崩溃后兜底 rescuer 默认 1 小时才回收，与 PRD 要求的 30 秒租约到期接管不匹配；所需的编排能力（Workflows）在付费 Pro 侧。**借鉴其「事务性入队」原则**：webhook 落库与 run 创建放同一事务。
- **gue**（vgarvardt/gue，MIT，小众单维护者）：transaction-level lock 要求整个执行期持有同一 DB 事务，分钟级 Agent 任务会把连接池占死。排除。
- **asynq**（hibiken/asynq，MIT）：README 原文确认基于 Redis，与「首版仅 PostgreSQL」冲突。排除。
- 自研可行性：所需 SQL 原语全是 Postgres 一等公民（SKIP LOCKED 领取、`UPDATE ... WHERE lease_epoch=$n` 续租与防僵尸写回、`ON CONFLICT` 幂等回执）；Neon/Prisma/Solid Queue 等均验证过该模式。M1 的「任务」是分钟级、可恢复的 Run，不是短 job——语义本来就该长在业务表上。

## 3. GitHub 接入库

| 库 | 版本 | License | Decision |
| --- | --- | --- | --- |
| google/go-github | v90.0.0 | BSD-3-Clause | **采用**：API 客户端 + webhook 验签解析（`ValidatePayload` / `ValidateSignature` / `ParseWebHook`，函数名以源码为准） |
| bradleyfalzon/ghinstallation | v2.19.0 | Apache-2.0 | **采用**：App JWT + installation token 自动获取/缓存/刷新，作为 go-github 的 RoundTripper |
| go-playground/webhooks | v1.x | MIT | **不采用**：功能与 go-github 内置重复，且已停滞约两年（2024-08 后无推送） |

GitHub App 权限按 PRD §16.1 最小申请：Metadata / Contents 读（固定 SHA 文件）、Issues 读、
Issues 写（仅发布器使用）。首版不申请管理/工作流/部署权限。

## 4. 数据访问与 Webhook 接入（Gin）

**Decision: sqlc + pgx/v5，不用 ORM**。强事务 + 行锁 + 状态机场景下社区主流答案是
「手写 SQL + 类型安全生成」；ent 的行锁仍要落 `.Modify()` 手写 SQL，GORM 控制力最弱。
（pgx MIT、sqlc MIT，均极活跃。）

**Gin webhook 惯用法**：`io.ReadAll` 先取 raw body → `ValidateSignature` 验 HMAC →
`ParseWebHook` 用 raw 解析，全程不触碰 `c.Request.Body` 二次绑定（body 只能读一次）；
handler 内只做「验签 + inbox_events 落库 + 事务性创建 Run」三步即返回 2xx，分析全部异步。

## 5. Python 智能层（AgentScope）

**Decision: agentscope==2.0.7（钉 minor）+ DashScope qwen-plus，自建 FastAPI 包裹，M1 不用框架状态持久化。**

- 版本事实：2.0.7.post1（PyPI 2026-08-28）；1.x 已停演进（1.0.21 与 2.0.0 同日发布）；
  仓库已迁 `agentscope-ai/agentscope`；Apache-2.0；**要求 Python ≥3.11**（本机 conda 3.13.5 满足）。
- **2.0 是大重写**，与网上 1.x 教程不兼容：结构化输出是 `await agent.reply(msg, structured_schema=PydanticModel)`，结果在 `result.structured_output`（dict），超轮次时为 `None`（`EXCEED_MAX_ITERS`），必须判空；1.x 的 `structured_model=`/`api_key=`/`state_dict` 写法在 2.0 全部失效。**写代码只认 docs.agentscope.io（2.0），不认 doc.agentscope.io（1.x）。**
- 模型接入：`DashScopeChatModel(model="qwen-plus")` + `DashScopeCredential(api_key=DASHSCOPE_API_KEY)`；2.0 已把 DashScope 修为 OpenAI 兼容，亦可换 `OpenAIChatModel` + 兼容端点（保留为换供应商备选）。qwen-plus 起步，疑难 Issue 手动升级 qwen-max；具体单价**未核实**，预算上线后按 token 用量复核。
- 结构化输出兜底（已知风险：issue #958 显示模型可能跳过约束工具直接输出纯文本，未确认修复）：外层 Pydantic `model_validate` 再校验，失败重试 1–2 次，仍失败**降级为「需要补充信息」结论**交给 Go，不把未校验文本当草稿。
- Agent 形态：第一版「预取固定 SHA 内容注入 prompt + 单次结构化输出」（零工具，延迟与不确定性最低）；第二形态注册只读工具 `read_repo_file(path)`（Go 内部接口按 SHA 提供）让 Agent 自主取证。Routing/Workflow 多 Agent 模式 M1 用不上。
- 状态管理：M1 单 Agent 一次性任务，每次请求新建实例；审计靠 Go 侧持久化请求/响应（2.0 内置持久化后端是 RedisStorage，引入即违背「不引 Redis」）。
- thinking 模式与结构化输出并用**未核实**兼容 → M1 对结构化输出场景关闭 thinking。

## 6. 前端（轻决策，按用户规则第 3 条不做深调研）

**Decision: Vite + React + TypeScript + 原生 fetch，不引组件库与状态库。**
M1 只有 3 个页面（仓库接入、Case 列表、任务详情+审批）；需要时再引入 TanStack Query。
Markdown 渲染必须 sanitize（PRD §6.4），选型到实现时核实具体库。

## 7. 选型总表

| 层 | 采用 | License | 自研部分 |
| --- | --- | --- | --- |
| Webhook/控制层 | Go 1.24 + Gin + go-github v90 + ghinstallation v2 | MIT/BSD-3/Apache-2.0 | 租约协议、审批状态机 |
| 数据 | PostgreSQL + pgx/v5 + sqlc | MIT | runs/attempts 表协议 |
| 智能层 | Python 3.13 + agentscope 2.0.7 + FastAPI + DashScope | Apache-2.0 | 引用收集与忠实性、外层校验降级 |
| 前端 | React+TS+Vite | MIT | — |
| 排除 | River/gue/asynq、Redis 系、ORM、OpenHands/SWE-agent、Sweep（license 禁止） | — | 理由见上文 |
