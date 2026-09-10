# T011 教学笔记：Webhook 入口——后端面试高频考点集合

> 对应文件：`internal/controller/webhook.go`、`internal/ids/`、`cmd/api/main.go` 接线
> 面试视角：这一任务覆盖「HTTP 语义 × 事务边界 × 幂等去重 × Go 闭包陷阱」四个
> 高频考点，逐个拆解。

## 1. Webhook 的响应语义：你在和一台"重试机器"对话

GitHub 投递 webhook 后只看一件事：**2xx 还是失败**。非 2xx 会按退避策略重投（最多 8 次）。
所以每个响应码都是对重投机器的"指令"：

| 场景 | 响应 | 对重投机器的含义 |
| --- | --- | --- |
| 受理并建 run | **202** | 已接单，别再发 |
| 重复投递（同 delivery_id） | **200** `deduplicated` | 这单我见过，处理过了 |
| 范围外/action 不符 | **200** `ignored` | 这类我不想收，别重投 |
| 验签失败 | **401** | 拒收（GitHub 会重试，但重试也被拒，无副作用） |

关键判断：**范围外事件返回 200 而不是 404**——返回错误码会让 GitHub 反复重投
一件我们永远不想处理的事，白耗双方。幂等语义的另一个考点：重复投递返回 200
（"已完成"）而非 202（"又接一单"）。

## 2. 幂等去重的实现与一个隐蔽语义陷阱

`inbox_events.delivery_id` 唯一索引 + `INSERT ... ON CONFLICT DO NOTHING RETURNING id`。
实测踩到的坑（测试当场抓住）：

**`ON CONFLICT DO NOTHING` 不抛 23505 唯一冲突错误**——它静默跳过插入，
导致 `RETURNING` 落空，pgx 返回的是 `ErrNoRows`！如果代码按"捕获唯一冲突错误"写，
重复投递会被当成数据库故障返回 500，触发 GitHub 无限重投。

```go
func translateDuplicate(err error) error {
    switch {
    case errors.Is(err, store.ErrNoRows):   // 主路径：RETURNING 落空
        return errDuplicateDelivery
    case store.IsUniqueViolation(err, "inbox_events_delivery_id_key"): // 兜底
        return errDuplicateDelivery
    ...
```

面试表达：**同一业务事实（重复）在驱动层可以有两种错误形态，必须在边界层翻译成
一种领域错误**。23505 分支保留是防御性兜底（查询改动时语义不漂移）。

## 3. 事务边界 = 业务边界

一次 webhook 触发四个写操作：登记事件 → UpsertCase → InsertRun → 事件关联 run。
全部包进 `WithTx` 的一个事务：

- **为什么必须同事务**：任何中间态都会破坏可追溯性——"有 run 无事件"让输入溯源
  （FR-3）断裂；"事件标 processed 但没 run"让重放补偿无从判断。要么全有要么全无。
- **什么不在事务里**：仓库范围检查（只读）。读操作放事务外既缩短事务持锁时间
  （1 秒预算），也不影响一致性——它只决定"要不要进事务"，本身不是状态变更。
- UpsertCase 的 `ON CONFLICT (repo_id, issue_number) DO UPDATE SET updated_at=now()`
  是"同 Issue 重开/再次触发时复用 case"的实现——**case 是长生命周期上下文，
  run 是一次执行**，一对多。

## 4. Go 语法考点：闭包里的 `:=` 变量遮蔽

```go
eventID := ids.New("event")
err = h.store.WithTx(ctx, func(q *db.Queries) error {
    evID, err := insertEvent(...)  // 若写 eventID, err := 会新建闭包局部变量
    eventID = evID                 // 必须用 = 回写外层
```

闭包内 `:=` 声明的是**新的局部变量**，外层同名变量不会被赋值。这里如果偷懒写
`caseID, err := q.UpsertCase(...)`，事务内部用的 caseID 是对的（UpsertCase 用参数），
但**返回给调用方的是外层旧值**——当 UpsertCase 命中已有 case 时（DO UPDATE 返回
已存在行的 id），调用方拿到的是一个从未落库的幽灵 ID。`go vet` 抓不到这种 bug
（它合法），只有测试断言"响应里的 run_id 真实存在于库里"才能抓到。

## 5. 其他语法/工程点

- **ID 生成**：`crypto/rand` 16 字节 hex + 前缀（`run-ab12…`）。不用自增计数器：
  ID 会进审批包与外部系统，可预测性是安全隐患；前缀让日志/SQL 一眼辨类型。
  `rand.Read` 失败直接 panic——熵源坏了是环境级不可恢复故障。
- **快照模式（FR-3）**：`input_snapshot` 在触发时刻钉住 title/body/updated_at +
  policy_version。之后 Issue 被编辑，本次执行不受影响——**读取固定快照 vs 读实时
  状态**是任务系统设计的经典分叉，快照换来可重放与可审计。
- **留痕优先**：验签失败、范围外、action 不符的事件全部落 `inbox_events`
  （signature_valid=false / process_status=ignored）。取证能力是事故复盘的底线，
  "没处理"和"没收到"必须可区分。
- **main.go 接线**：私钥在启动时读入并构造 ClientFactory（fail-fast：PEM 坏了
  立刻退出而不是等第一个 webhook）。

## 6. 自测证据

6 个集成子测试（真实 PG，每包专属库）：opened 受理落库三表齐、同 delivery 重投
去重、edited 不建 run、范围外留痕、验签失败 401 留痕、不同 issue 各建 case/run。
全套 `go test ./...` 绿。

## 7. 面试自问自答（复习用）

- Q: 为什么 webhook 处理器不做重活？ A: 1 秒预算 + 快速 ACK 模式；落到 QUEUED
  后由 runner 异步消费（生产者-消费者解耦）。
- Q: delivery_id 去重和 run_commits 的 commit_id 幂等是什么关系？ A: 两层幂等——
  入口层防重复触发（不重复建 run），出口层防重复提交（同 commit_id 回显旧结果）。
- Q: 如果事务提交后、HTTP 响应前进程崩溃？ A: GitHub 没收到 2xx 会重投，重投命中
  delivery_id 去重返回 200——业务已提交、响应丢失的场景被入口幂等兜住。
