# AliGo 架构学习与 DevFlow 映射

核对日期：2026-08-31。本文记录参考依据与设计取舍，不代表 DevFlow 已经实现或达到生产规模。

## 1. 原文与已核验事实

用户提供的 [微信公众号原文](https://mp.weixin.qq.com/s/3MXwawLU716HM5aGARlTXA) 无法直接抓取；已阅读 [AgentScope 官方同源全文](https://agentscope.io/blog/alibaba-business-travel/)，该页面的原文链接指向同一微信地址。

AliGo 使用 Python / AgentScope 承担规划、记忆与模型调用，Java 承担鉴权、MTop/MCP 等外围服务；文中还介绍了 Routing/Handoffs、规则与模型分流、按需共享上下文、Hooks 状态输出和 Langfuse 观测。

原文没有给出完整的跨语言协议、数据库写入所有权、动态 DAG、租约隔离与审批恢复规范。以下均是 DevFlow 的适配设计，不能作为 AliGo 原系统的事实引用；也不直接套用文中的业务效果数字。

## 2. DevFlow 的职责映射

| 职责 | DevFlow 设计 | 具体边界 |
| --- | --- | --- |
| 外围服务 | Go API、鉴权、GitHub Webhook、限流、任务下发与 SSE | 管理执行资格和外部入口，不承担 Agent 内部编排 |
| 智能逻辑 | Python + FastAPI + AgentScope | 意图、规划、角色协作、上下文、重规划与结果聚合 |
| 确定性执行 | Python DAG 校验与 asyncio 有界调度 | 只有一套节点执行器；模型不能跳过依赖、必要检查与预算 |
| 工具访问 | Go GitHub 工具服务，Python 注册工具包装器 | Go 校验 Run 范围并保管凭据；首版 HTTP，MCP 按需增加；模型不直接持有发布能力 |
| 隔离代码执行 | 独立 Go Runner 与固定 ExecutionProfile | 实际修改、封存与测试；无 GitHub/模型凭据；外部不可信代码使用独立临时 VM 等额外隔离 |
| 持久执行 | Go 账本 + Python 检查点协议 + PostgreSQL | 租约、提交幂等、计划版本、取消和恢复语义由项目定义 |
| 人工审批 | Go ApprovalBundle / Action / Publisher | Issue 回复、Review COMMENT、分支和 Draft PR 发布绑定获准内容与补丁版本；结果不明先核对；不自动合并 |
| 可观测性 | AgentScope / Go OpenTelemetry + Langfuse | 传播 Trace 上下文；持久状态事件与调试 Trace 分开 |

混合分层不要求每次模型调用都跨语言往返。Go 下发一次有边界的 Run，Python 在其中完成 Agent 协作；检查点、业务事件和受限工具请求才经过 Go。任务、Runner、补丁验证、审批与发布协议详见 [完整 PRD V3.0](<D:/desktop/jobs/projects/调研/DevFlow 完整 PRD.md>)，概览见 [核心技术架构 PRD](<D:/desktop/jobs/projects/调研/DevFlow 核心技术架构 PRD.md>)。

## 3. 框架能力与项目自研边界

AgentScope 提供角色与工具开发基础、消息/记忆、Hooks，以及 [状态保存与加载](https://doc.agentscope.io/tutorial/task_state.html)。其对象快照是恢复输入，不自动证明跨服务任务、外部副作用和进程故障已得到正确处理。

项目需要自行实现或验证：结构化计划约束、动态 DAG、计划切换与结果复用、Run 租约与取消、原子提交和幂等恢复、隔离修复/测试、补丁版本与批准绑定、GitHub 写入核对。框架组件的实际版本和已覆盖范围应在开发时记录，不把这些能力全部归为“从零自研”。

[AgentScope 官方 Tracing 文档](https://doc.agentscope.io/tutorial/task_tracing.html) 给出了 OpenTelemetry 与 Langfuse 接入方式；因此删除旧简历里的 Eino Callback 桥接。接入后仍需验证 Go 到 Python、后台任务及自定义工具的关联关系。

## 4. 简历与实施约束

候选表达：参考 AliGo 的公开分层实践，采用 Go 服务层与 Python / AgentScope 智能层，围绕研发任务实现动态编排及受控执行。只有已完成并有代码、轨迹或报告支撑的部分，才可改写为投递版成果。

“参考生产实践”说明设计来源，不说明 DevFlow 已具备同样的负载、安全保障或业务效果。两层架构也不自动要求微服务平台、Redis Streams 或全量 GitHub 镜像。

当前主线按用户访谈调整为 Issue 解答或隔离修复、测试验证、人工批准发布分支/PR，并包含 PR Review 与 CI 调查。自有测试仓库可单独启用限时、限动作的自动发布策略；AI 审查不等于人类审批，任何模式均不自动合并。这些是 DevFlow 的产品选择，不归因于 AliGo 原文。LearnPilot 保持 Python 应用，不随 DevFlow 引入 Go 服务。
