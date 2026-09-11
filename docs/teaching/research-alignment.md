# 架构设计 × 业界实践对照表（面试弹药库）

> 目的：本项目每个自研协议设计都有业界权威锚点对照——面试时既能讲「我为什么这么设计」，
> 也能引用来源证明「这 aligns 主流实践」。来源全部经 agent 实际抓取核实（2026-09-11）。
> 维护约定：每次协议级设计决策完成后，由调研 agent 补充新行。

## 1. 分布式任务协议（后端）

### 1.1 lease_epoch ≡ Kleppmann 的 fencing token

| | 我们 | Kleppmann |
| --- | --- | --- |
| token | `lease_epoch`，每次领取 `+1`，单调递增 | 「每次获取锁时单调递增的数字」 |
| 安全性来源 | 写回 SQL `WHERE owner+epoch`，0 行=拒收 | 「存储侧记住已处理更高 token 的写，拒绝旧写」——**全部安全性来自存储侧强制检查** |

来源：[How to do distributed locking (2016)](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)、DDIA 第 8 章。

**面试讲法**：*「我们的租约协议里 lease_epoch 就是 Kleppmann 说的 fencing token——安全性不依赖客户端自查，全部由 PostgreSQL 的条件 UPDATE 强制：旧纪元的写回 0 行命中即拒收。而且 token 发放者就是 PG 行本身，不存在 Redlock 那种多节点时钟共识问题，比 Kleppmann 批评的对象更强。」*

**Kleppmann 点名的盲区（我们如何回应）**：fencing 只保护「能检查 token 的资源」；写 GitHub 评论是不可检查 token 的外部副作用——锁对它无效。我们的补偿：发布前重核（尽力而为）+ RECONCILING 核对（真正防线）+ 绝不自动重发。

### 1.2 领取 SQL ≡ Graphile Worker 同构

```sql
UPDATE ... WHERE id = (SELECT id ... FOR UPDATE SKIP LOCKED LIMIT 1)
```
与 [Graphile Worker 的 getJobs](https://github.com/graphile/worker/blob/main/src/sql/getJobs.ts)（MIT）完全同构的单条原子领取。对比 [pg-boss](https://github.com/timgit/pg-boss/blob/master/src/plans.ts)：`FOR UPDATE OF j SKIP LOCKED` + 心跳失联检测 + 重试预算 + DLQ。

**关键差异（讲进取取感）**：
- Graphile 崩溃恢复默认 **4 小时**纯时间兜底；我们 30s 租约 + 5s 心跳 + Sweeper，恢复延迟 ~45s——量级接近 [River Pro 的 active job rescue](https://riverqueue.com/blog/active-job-rescue)（30s 心跳 + 60s 边际）。
- [Brandur: Postgres Job Queues & Failure by MVCC](https://brandur.org/postgres-queues)：队列表是高 churn 表，长事务阻止 VACUUM → 锁延迟 15 倍劣化。我们已做：领取查询走部分索引、终态条件写回；**待做**：心跳降频/挪 UNLOGGED（量级未到，记为已知权衡）。
- [River](https://riverqueue.com/blog/active-job-rescue)：救援查询特意不写主表（"produces no new dead tuples"）——死元组治理的标杆做法。

### 1.3 commit 协议 ≡ Stripe 派生式幂等键

| | 我们 | Stripe |
| --- | --- | --- |
| key | `commit_id = stage + run`（业务结构派生） | 官方文档点名认可两种：随机 UUID **或**「从业务对象派生（如购物车 ID）」 |
| 同 key 异 body | `ErrPayloadConflict`（409） | `error_params_mismatch`（409），定性为客户端 bug |
| 在途并发 | 联合主键插入序列化 | `error_request_in_progress`（409） |
| 保留期 | 永久（(run, stage) 约束下有界） | 24h 清理（我们不需要） |

来源：[Stripe API - Idempotent requests](https://docs.stripe.com/api/idempotency_requests)、[Stripe 工程博客 (2017)](https://stripe.com/blog/idempotency)、[Brandur 的 PG 实现版](https://brandur.org/idempotency-keys)。

**边界语义差异（我们更干净）**：Stripe 缓存失败结果（连 500 也回放）；我们的 run 失败**不占用** commit_id（FailRun 走 runs 表终态，不落 run_commits）——失败重试干净，不会撞「同 ID 异内容」。

### 1.4 runs 表 ≡ transactional outbox 的合法变体

[microservices.io 的 outbox 模式](https://microservices.io/patterns/data/transactional-outbox.html)：业务表与 outbox 同事务写，relay 异步投递。我们的形态：**webhook 事务内建 QUEUED run（runs 表既是业务表也是 outbox）+ runner 轮询（polling publisher）**，交付目标是内部执行而非 broker。M1 单机不需要 CDC/Debezium 的 log-tailing。

**outbox 模式对我们的一条硬要求（已满足）**：入边必须幂等——GitHub webhook 是 at-least-once 投递，我们按 `delivery_id` 唯一索引去重（AC45），对应 Debezium 方案里消费端的 MessageLog 去重表。

### 1.5 「响应丢失只核对不重发」≡ Stripe/Brandur 的标准立场

理论根基：两将军问题/exactly-once 不可能性（[You Cannot Have Exactly-Once Delivery](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/)）。业界的分界线是**接收方有没有幂等能力**：

- Stripe 自己支持幂等键 → 官方建议「带同 key 大胆重试直到拿到结果」；
- GitHub 创建评论 **无任何幂等机制**（重发=重复评论+重复通知）→ [Brandur 原则](https://stripe.com/blog/idempotency)：「对外部非幂等 API，只有错误明确允许时才重试，否则标记失败」；Stripe 对 500 也要求 "treat as indeterminate... reconcile the results"。

**面试讲法**：*「我核实过 GitHub 评论 API 没有幂等键，所以不确定结果时我只做 reconciliation（按 bot 身份+内容全文核对远端），查无实据转人工——这是 Stripe 对外部非幂等 API 的标准立场，不是过度保守。」*

## 2. Agent 工程（第二轮调研已归档）

### 2.1 结构化输出多层兜底

- arXiv 2606.09395（2026，结构化输出实证研究）：grammar-constrained decoding **necessary but insufficient**——语法错误几乎消灭，结构/语义错误依然存在。我们的 Pydantic 校验 + 重试预算 + NEEDS_INFO 降级正是论文结论的工程化。
- arXiv 2606.25605「约束税」：schema 约束会静默抑制部分模型能力——不能默认"开了 JSON mode 就更可靠"，必须独立评测 + 运行时兜底。
- OpenAI cookbook：strict mode 仍有 `refusal` 字段（官方承认约束可被打破并给显式信号）；GPT-3 时代但仍是官方建议的 halter 哲学——"宁可无答案，不可编造"（no answer preferable to hallucinated guess）。

### 2.2 防循环（bot 判定）——已修复的 P0

- claude-code-action（Anthropic 官方）：bot 判定**不靠名字后缀，靠 Users API `type` 字段**（`actorType !== "User"` 即拒），注释专门点名 Copilot 这类不以 `[bot]` 结尾的 App。
- pr-agent：用 webhook payload 自带的 `sender_type == "Bot"`（零 API 调用）。
- **我们的修复**：`isBotSender` 优先 `sender.type == "Bot"`，`[bot]` 后缀仅兜底（commit 待打）。
- GitHub 平台层先例：GITHUB_TOKEN 触发的事件不会递归触发 workflow——"平台层防递归"是公认必要。
- **我们超出业界**：delivery_id 去重在两个参照项目均不存在。

### 2.3 引用可靠性（citation/grounding）——verbatim gate 已落地

- Gemini cookbook（Citation Faithfulness Check）：引用失败三分类 **Fabricated / Frankenquote / Misattributed**；两级瀑布 = **0-token verbatim gate（纯代码，fail-closed）→ 昂贵判官**——"fabrications never reach it, so they cost zero tokens"；**"A found citation is not yet correct"**（存在性≠支持性，支持性交人工判官=我们的审批门）。
- Anthropic Citations API：引用是 API 服务端结构化对象（char offset + cited_text + document_index 三重绑定），模型无法凭空产出未在文档中的 cited_text。
- CAMS（arXiv 2606.23989，2026）：可归因要 **by construction**——我们的 path+sha+lines 是 schema 必填结构（构造出来的不变量），且 sha 比商业 API 多钉了内容版本。
- LayerRAG-Bench（arXiv 2607.27353）：**引用存在≠引用仍有效**（stale evidence）——sha 把证据钉在 commit 上正是防这个。
- Sufficient Context（Google，ICLR 2025）：强模型上下文不足时倾向硬答不弃权——**NEEDS_INFO = selective generation/abstention 的工程化**。
- **我们的落地**：`gateEvidence` 逐字门（0 token，Go 侧）：quote 空白归一后必须逐字出现在预取文件 + 行号必须框得住 quote；ANSWER_READY 失去全部证据 → 降级。

### 2.4 成本护栏

- OpenAI cookbook（per-run spending controller）：**成本无法确认即永久熔断**（UncertainCharge），而非记 0 继续；reserve→call→settle→refund 模式。
- Arize 案例（2026-08）：生产 agent 两个隐藏重试循环 **43 次重复工具调用、root span 全绿**——"agent 的 bug 不表现为报错，而表现为看似在推进的行为"，只有预算限制止损。⇒ 24 次调用预算的最强实战论据。
- Anthropic 多 agent 系统（2025-06）：agent 耗 ~15 倍 token、token 量解释 80% 性能方差——逐调用记账是成本可解释性的前提。
- Context rot（arXiv 2607.17937）：上下文 ~11K→~299K 时 pass rate 8/10→3/10——**900s 上限同时是质量护栏**。
- 差距（记为演进项）：金额预算维度（reserve/settle）与"费用取不到=熔断"语义（我们的合同层已强制 usage 必填，token 缺失时估算——比熔断宽松，记为已知取舍）。

### 2.5 编排与 durable execution 趋势

- Restate（Flink 作者执笔，2025-06）：**"Agent 就是一个分布式系统循环"**——重试可能重复副作用，解法是 journal+replay；人工审批 = durable promise 的自研等价物。
- Restate（2026-06）：PG/SDK 式 checkpoint 是 restart 不是 recovery，需要 step 级 journal 与 **fencing token**——我们的 epoch 又一次被点名背书。
- Temporal Agent Harness（2026-08）：审批策略 **"enforced in the harness, not in model instructions"**——我们的门在 Go 控制层，不在 prompt。
- DBOS（2026-05/06）：**"Postgres Is All You Need for Durable Workflows"**（workflow 状态 checkpoint 进库，DB 本身就是编排器）+ 实测 SKIP LOCKED + 部分索引到 30K workflows/s——Go+PG 架构的正面论证与规模化证据。
- LangGraph Persistence：PostgresSaver checkpoint + HITL interrupt——我们等于手写了它的最小子集。

## 3. 已识别的改进项（落地跟踪）

| # | 项 | 级别 | 状态 |
| --- | --- | --- | --- |
| 1 | commits 插入的 epoch 守卫 | 调研标记必须修 | **无需修**：CommitRunResult 单事务里 CompleteRun 0 行即回滚回执（T013 测试实证 receiptCount=0），原子性已覆盖 |
| 2 | webhook delivery 去重 | 必须修 | **已有**：delivery_id 唯一索引 + ON CONFLICT（T011 测试覆盖） |
| 3 | Sweeper/心跳守卫完整性 | 必须修 | **已落地**：心跳 WHERE 补 `status='RUNNING'`，测试 TestHeartbeatStatusGuard 验证迟到心跳不复活 |
| 4 | 失败不占 commit_id | 必须修 | **已满足**（设计即如此） |
| 5 | 领取排序补 id 决胜 | 建议 | **已落地**（0002 迁移） |
| 6 | 毒 run 重试预算（attempts/max） | 建议 | **已落地**（0002 迁移 + 清道夫终结，测试 TestRetryBudget） |
| 7 | 心跳 churn 治理（UNLOGGED 副表） | 建议 | 量级未到，记为已知权衡 |
| 8 | 核对键限定 [bot] 账号 | 建议 | **已落地**（T024 review 修复） |
| 9 | bot 判定用 sender.type | agent P0 | **已落地**（isBotSender，12 用例含 Copilot 类命名） |
| 10 | 引用 verbatim gate | agent P0 | **已落地**（gateEvidence，5 用例：fabricated/行号越界/空白容差等） |
| 11 | 成本不可确认即熔断 | agent P0 | 部分满足：合同层强制 usage 必填；token 缺失走估算——记为已知取舍（M1 单操作者可控） |
| 12 | 金额预算（reserve/settle） | agent P1 | 演进项（次数预算已有，金额维度待加） |
| 13 | 降级原因码 / 反馈重试 | agent P1 | 演进项（需改合同 schema，走合同先行流程） |
