# T010 教学笔记：Runner 骨架——让任务真正"跑起来"的引擎

> 对应文件：`internal/controller/worker.go`、`cmd/runner/main.go`、`internal/testutil/`（+测试）
> 一句话：把 T007 的租约 SQL 变成一台会呼吸的引擎——领取循环、心跳保活、失权即停、
> 终态写回带 fencing；Phase 2 至此闭环，真实种一条 run 能被自动执行完成。

> 本任务同样走「skills + reviewer agent」流程。review 抓出 3 条 P1
> （心跳无超时→失权检测失效、终态写回确定性失败→毒循环、字节截断破坏 UTF-8→
> FailRun 被拒），全部修复后才提交——§5 记录这三个问题，值得逐个理解。

## 1. 双进程架构：为什么 api 和 runner 要分开跑

webhook 的硬指标是 **1 秒内持久化并响应**（PRD §26.2）；而 run 执行是分钟级的长任务。
塞在一个进程里，一次慢执行就能把 HTTP 响应拖过线。分开后：
- api 只做"接单落库"（快路径），runner 只做"领单干活"（慢路径）；
- 各自独立伸缩/重启：runner 崩了 webhook 照收，api 重启不影响执行中的 run
  （租约协议保证被接盘）；
- 代价是 `config.Load` 全量校验被两个进程复用（runner 用不到 GitHub 配置也一起验）——
  M1 单操作者共享一份 `.env`，这是可接受的简化，注释里已说明。

## 2. worker.go：领取循环的三种命运

```go
for {
    claim, err := w.store.ClaimRun(短超时ctx, &owner)
    switch {
    case errors.Is(err, store.ErrNoRows): // 队列空 → 睡 2s
    case err != nil:                       // 数据库抖动 → 记日志退避 3s
    default: w.process(ctx, claim)         // 领到 → 干活
    }
}
```

**错误的三分类是循环健壮性的核心**：`ErrNoRows` 是正常业务状态（空队列）；
其他错误是基础设施抖动（退避重试，绝不让进程崩）；领取成功才进执行路径。
`sleepCtx`（select ctx.Done + timer）保证"睡着也能被叫醒"——如果用裸
`time.Sleep`，停机时 worker 会多睡满 2 秒才退出。

### 领取查询也要带超时（review P1-1）

TCP 黑洞式网络分区下，一次无超时的 DB 查询会挂到 OS 级超时（分钟级）。挂住的
恰恰是心跳循环的话：租约过期 → 别人接管 → 你还活着但永远发现不了失权——
**失权检测器自己失权**。所以领取和心跳都包了 3s 的 `context.WithTimeout`。
教训：**凡依赖外部资源的调用，超时是参数的一部分，不是可选优化**。

## 3. process：心跳、失权与 fencing 的三重奏

```go
claimCtx, cancel := context.WithCancel(ctx)
go w.heartbeatLoop(claimCtx, cancel, claim)  // 心跳在旁路续命
outcome, err := w.safeExecute(claimCtx, claim)
```

三层 context 各司其职：
- **ctx**（进程级）：Ctrl+C 取消，一切停止；
- **claimCtx**（单 run 级）：心跳 0 行（失权）时只取消它——正在执行的钩子
  通过 ctx 感知并收手，worker 主循环不受影响；
- **callCtx**（单次 DB 调用级）：3s 上限，防单查询挂死。

`context.CancelFunc` 幂等，process 的 defer 和心跳 goroutine 共用同一个 cancel
是安全的——两处取消只有一处生效，无竞态。

### 失权语义的一个微妙点（review P2-4 澄清）

写回 SQL 的 WHERE 校验 `owner + lease_epoch`，**不校验 status**。这意味着：
只有**接管真的发生**（epoch+1）后，旧纪元的写回才会被 0 行拒绝；如果租约只是
过期（RECOVERING）但没人接管，原执行者写回依然成功——这在协议上是**对的**
（它仍是唯一干活方），但注释必须把"epoch 是唯一权威 fencing"讲透，不能笼统说
"写了也会被拒"。钩子返回时若 `claimCtx.Err() != nil`，一律放弃写回：
失权和停机都交给租约协议收尾。

### 防毒循环：终态写回失败的兜底（review P1-2）

如果钩子返回的 `outcome` 撞了 CHECK 枚举（比如拼写错），`CompleteRun` 必然失败，
run 停在 RUNNING → Sweeper 15s 后翻 RECOVERING → 2s 后又被领取 → 又失败……
**约 45 秒一次的永动循环**。修复：CompleteRun 失败后用同一 owner+epoch 尝试
写 FAILED"钉死"——确定性失败最多执行两次就进终态，绝不让毒 run 循环烧预算。

### UTF-8 截断的坑（review P1-3）

`err.Error()[:500]` 按字节切，切在汉字中间产生非法 UTF-8，PG 直接以 22021 拒收——
**失败信息反而写不进库**，和毒循环叠加成完美风暴。修复：切完从尾部剥掉不完整
字节（`utf8.ValidString` 校验循环）。本仓库错误消息以中文为主，这个坑几乎必然踩。

## 4. 测试基建的升级：每包一个临时数据库

全套 `go test ./...` 时**多个包的测试二进制并发跑同一个库**——store 包的清场
DELETE 会删掉 controller 包刚种的夹具，竞态只在全量跑时偶现（单跑永远绿），
是最阴的集成测试坑。修复：`testutil.Database(t)` 为每个测试二进制
`CREATE DATABASE devflow_test_<pid>_<nanos>`，结束时 `DROP ... WITH (FORCE)`。
从此包间零干扰，`go test ./...` 想跑几遍跑几遍。

另两个测试手法：
- **Eventually 轮询断言**：异步行为（worker 落库）不靠 sleep 猜时机，靠条件收敛；
- **epoch==1 断言**：比"数钩子调用次数"更硬——能抓到"ClaimRun 的 WHERE 被改坏
  导致 COMPLETED 可再领取"这类回归。

## 5. Review 抓住的三个 P1（值得记住的失败模式）

| P1 | 失败模式 | 修复 |
| --- | --- | --- |
| 心跳/领取无超时 | **检测器自身挂死** → 失权检测失效 | 所有外部调用包短超时 |
| CompleteRun 确定性失败 | **重试路径 × 确定性失败 = 毒循环** | 失败兜底写 FAILED 钉死 |
| 按字节截断中文 | **修复动作本身非法**（UTF-8 拒收） | utf8 边界感知截断 |

三者共性：单看每处代码"都还行"，组合起来才炸——这正是独立 reviewer 的价值。

## 6. Checkpoint 证据（Phase 2 验收）

| 场景 | 结果 |
| --- | --- |
| `api` 启动 + `/healthz` | `{"database":true,"status":"ok"}` HTTP 200 ✅ |
| `runner` 启动 | JSON 日志：数据库就绪 → runner 已启动（owner=runner-25764）✅ |
| 种 run-cp（QUEUED） | 14s 内被自动领取 → 占位钩子执行 → `COMPLETED/ANSWER_READY`，epoch=1 ✅ |
| 迁移可重复执行 | TestMigrateIdempotent ✅ |
| 全套测试 ×2 | 全绿（每包专属库隔离）✅ |

## 7. 下一站预告

Phase 3（US1）开工：T011 webhook 入口把 GitHub 事件变成 QUEUED 的 run，
T014 把占位钩子换成「调 Python 分析 + 原子提交」的真实现——引擎马上拉真货。
