# T019 教学笔记：并发协议测试——用 goroutine 导演生产事故

> 对应文件：`internal/controller/lease_concurrency_test.go`
> 面试视角：**并发正确性怎么测**——竞态无法靠单线程测试发现，
> 这三个测试是「把竞态变成可重复断言」的模板。

## 1. 并发领取互斥：16 抢 8，恰好 8 次成功

```go
for w := range workers {  // 16 个 goroutine 同时 ClaimRun
    go func(w int) {
        for {
            claim, err := st.ClaimRun(ctx, &owner)
            if errors.Is(err, store.ErrNoRows) { return }
            ...计数 + 重复检测...
        }
    }(w)
}
```

断言的是**计数与唯一性**：成功次数恰 = 8（不多不少——多说明重复领取，
少说明丢任务）；`claimedBy` map 检测同一 runID 被两个 owner 领取。
`FOR UPDATE SKIP LOCKED` 的正确性在 T007 只有理论推演，这里第一次在
**真实 PG + 真实并发**下被证实。

注意 `t.Errorf`（不是 Fatalf）：并发 goroutine 里 Fatalf 只终止当前 goroutine，
其他 goroutine 会被拖成悬挂——**并发测试里 Errorf + 收口等待是标准姿势**。

## 2. 时间相关断言的两个技巧

心跳续租测试要证明"租约前进了"，两个坑：
- **同刻比较假阳性**：`before` 和 `after` 可能在同一毫秒，`after > before` 恒真。
  解法：先 `time.Sleep(1.1s)` 让墙钟真的前进；
- **环境抖动假阴性**：断言"剩余 ≈ 30s"时给宽区间（20–40s），慢 CI 不会误报。

## 3. 接管全链路：把生产事故拆成 SQL 步骤

```
A 正常领取（epoch=1）
  → SQL 把 lease_expires_at 拨到过去        （导演"网络分区"）
  → ExpireStaleRuns 清道夫翻 RECOVERING      （恢复机制启动）
  → B ClaimRun 接管：同 run、epoch=2        （新纪元开始）
  → A 的心跳 0 行                            （失权信号）
  → A 的提交被拒                             （AC24 fencing）
  → B 提交成功，回执记 B 的 epoch            （AC23 权威转移完成）
```

对比 TestCommitRunResult 的单点语义测试，这里是**完整时间线**：每一环的
状态（status/owner/epoch/回执）都被断言。生产里"僵尸执行者"需要进程暂停
才能复现，测试里三行 SQL 就能导演——**集成测试的本质是可控地制造不可能现场**。

## 4. 路径偏差说明

tasks.md 原定 `tests/control/lease_test.go`（模块外目录），但 Go 的 `internal/`
包**不允许被模块外导入**——集成测试必须与被测代码同模块。
落在 `internal/controller/` 与其余协议测试同包，测试逻辑无偏差。
（这是 Go 模块布局的硬约束，不是偷懒。）

## 5. 面试自问自答

- Q: 为什么不 mock 数据库测并发？ A: 并发互斥的裁判是数据库的行锁——mock 了
  数据库等于把被测系统也 mock 掉了。SKIP LOCKED 语义只有真 PG 能作证。
- Q: 怎么防止 goroutine 泄漏拖垮测试？ A: 每个 goroutine 收到 ErrNoRows 即退出
  （有界循环），主测试 wg.Wait 收口；不用无限轮询 + 超时的松散模式。
- Q: SKIP LOCKED 和消息队列（如 Redis BRPOP）的互斥有什么区别？ A: 前者互斥
  由 ACID 事务保证，任务状态与业务数据同库同事务（一致性免费）；后者要自己
  处理"取出后崩溃"的可见性超时——这正是 M1 不引 Redis 的核心理由。

## 6. 自测证据

三个测试全绿；全套 `go test ./...` 绿。US1 的 9 项任务（T011–T019）至此全部完成。
