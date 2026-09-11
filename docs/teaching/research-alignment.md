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

## 2. Agent 工程（详见 agent 调研文档，待补充）

- 结构化输出四层防线 ↔ 各家官方 structured output 指南（调研中）
- 证据引用可核对 ↔ citation/grounding 工程（调研中）

## 3. 已识别的改进项（落地跟踪）

| # | 项 | 级别 | 状态 |
| --- | --- | --- | --- |
| 1 | commits 插入的 epoch 守卫 | 调研标记必须修 | **无需修**：CommitRunResult 单事务里 CompleteRun 0 行即回滚回执（T013 测试实证 receiptCount=0），原子性已覆盖 |
| 2 | webhook delivery 去重 | 必须修 | **已有**：delivery_id 唯一索引 + ON CONFLICT（T011 测试覆盖） |
| 3 | Sweeper/心跳守卫完整性 | 必须修 | 部分：Sweeper WHERE 已带状态+时间；**待做**：心跳 WHERE 补 `status='RUNNING'`（防 reaper 翻转后迟到心跳复活） |
| 4 | 失败不占 commit_id | 必须修 | **已满足**（设计即如此） |
| 5 | 领取排序补 id 决胜 | 建议 | 待做 |
| 6 | 毒 run 重试预算（attempts/max） | 建议 | 待做（防崩溃循环） |
| 7 | 心跳 churn 治理（UNLOGGED 副表） | 建议 | 量级未到，记为已知权衡 |
| 8 | 核对键限定 [bot] 账号 | 建议 | **已落地**（T024 review 修复） |
