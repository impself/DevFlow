# T013 教学笔记：租约协议收口——提交回执与取消检查

> 对应文件：`internal/controller/lease.go`（+5 个协议测试）、`runs.sql` 增查
> 面试视角：**分布式任务系统的"结果提交协议"**——幂等、防僵尸、防数据矛盾，
> 三个 AC 各对应一条可测试的规则。

## 1. 协议全景：四个动作，一个文件

租约协议共四个动作，现在全部有归属：

| 动作 | 位置 | 关键机制 |
| --- | --- | --- |
| 领取 claim | `runs.sql` + worker 循环 | `FOR UPDATE SKIP LOCKED` + epoch+1 |
| 心跳 heartbeat | worker goroutine | 5s 续租；0 行 = 失权 |
| **提交 commit**（本任务） | `lease.go` | 幂等回执 + epoch fencing，单事务 |
| 取消检查 cancel | `lease.go` | `CANCEL_REQUESTED` 轮询 |

## 2. 提交协议的三条规则（AC23–26）

`CommitRunResult` 在**一个事务**里做两件事：插回执（run_commits）+ 落终态（runs）。
三种可能结局对应三条协议规则：

```
首次提交：  回执插入成功 → CompleteRun（owner+epoch 校验）→ COMMITTED    (AC23)
重放提交：  回执冲突被忽略 → 比对 payload_hash
              相同   → CommitDuplicated，什么都不改                      (AC25)
              不同   → ErrPayloadConflict（=HTTP 409），事务回滚        (AC26)
僵尸提交：  回执插入成功但终态 0 行（epoch 已变）→ ErrStaleEpoch，回滚   (AC24)
```

**为什么必须单事务**：僵尸提交（规则三）如果回执和终态分开写，会留下"回执在、
终态错"的半提交——回执是审计凭证，僵尸的凭证不该被保存。事务回滚让僵尸
**在数据库里什么都没留下**（测试断言了这一点：receiptCount == 0）。

**幂等键设计**：`(run_id, commit_id)` 联合主键 + `payload_hash` 内容比对。
键相同内容不同 = 数据矛盾（409），**绝不能静默覆盖**——否则重试方可能用错误
内容顶替正确结果。这是"幂等键 + 内容哈希"双保险模式，Webhook 重试、支付回调
设计通用。

## 3. 重放路径的一个细节：为什么不再动终态

`CommitDuplicated` 分支只返回回执、不执行 CompleteRun——因为回执和终态在同一
事务里落库，**回执存在 ⟹ 终态已落**（不变量）。重放时如果再跑一遍终态写回，
既是多余写，还可能撞上"终态已被接管方改写"的窗口。

不变量思维：与其到处防御，不如用事务结构保证"某些状态组合不可能存在"，
然后放心依赖它。

## 4. 取消检查：合作式取消的正确姿势

`CancelRequested` 是给执行方在**长步骤之间**调用的钩子。取消是合作式的：
控制面只把状态改成 `CANCEL_REQUESTED`，执行方在下个检查点自愿停手——
不杀线程、不抢锁。执行方停手后不写终态（交给取消方/清道夫），避免取消方和
执行方竞写同一行。

查不到 run 返回"未取消"而非报错：**辅助检查永远不该比主流程更致命**。

## 5. 测试设计：协议测试造"不可能现场"的方法

旧纪元提交测试要制造"接管已发生但旧执有者还来提交"的现场——生产里这需要
进程暂停，测试里用 SQL 直接导演：

```sql
UPDATE runs SET lease_expires_at = now() - interval '1s';  -- 手动过期
-- ExpireStaleRuns 清道夫翻 RECOVERING
-- worker-b ClaimRun 接管（epoch=2）
-- worker-a 拿旧 claim 提交 → ErrStaleEpoch
```

租约协议的每个角色（过期、清道夫、接管、僵尸写回）都能用 SQL 单独导演——
这正是 T006 把协议做进数据库层的红利。

## 6. 面试自问自答

- Q: 这和"先查后写"防重复比好在哪？ A: 先查后写在并发下有竞态窗口（TOCTOU），
  唯一约束 + ON CONFLICT 是数据库原子保证，无窗口。
- Q: ErrStaleEpoch 时执行方该做什么？ A: 立刻停止一切动作（包括模型调用），
  什么都不写——终态归接管方。这正是 worker.go 里心跳失权逻辑的兜底双保险。
- Q: commit_id 怎么生成？ A: T014 由执行流按 `(run, stage, seq)` 派生，
  同一阶段同一序号的重试天然同 ID——幂等键来自业务结构，不是随机数。
