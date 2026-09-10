# T007 教学笔记：sqlc 查询集——租约协议的心脏

> 对应文件：`sqlc.yaml`、`internal/store/queries/{inbox_events,runs,cases}.sql`、
> `internal/store/db/*`（生成代码，入库）
> 一句话：把"谁能领取任务、谁有权提交结果"的裁判规则写进 SQL——
> 三条查询撑起整个租约协议（SKIP LOCKED 领取、epoch 心跳、幂等回执）。

## 1. sqlc 的世界观：SQL 是真源，Go 是编译产物

三种数据访问流派：

| 流派 | 代表 | 何时出错 | M1 判断 |
| --- | --- | --- | --- |
| ORM | GORM | 运行时（关联加载炸在半夜） | 宪法明确排除 |
| 手写 scan | pgx 裸写 | 运行时（列序对错靠肉眼） | 容易错 |
| **sqlc** | 本任务 | **编译期**（SQL 解析失败/类型不符直接报错） | ✅ |

sqlc 读你的 SQL + schema，**生成**类型安全的 Go 查询函数。SQL 写错（列名拼错、参数
个数不对）在 `sqlc generate` 时就炸，不用等第一次请求。宪法原则 VIII 选型时对比过
River/gue 等队列库——引库是把协议交给别人；sqlc 只生成"SQL 的类型安全包装"，
协议（租约、幂等）100% 仍是自己的。

`sqlc.yaml` 里有个关键决定：`schema: "internal/store/migrations"`——**直接把迁移目录
当 schema 源**。表结构只有一份真相（0001_init.sql），查询与表结构在编译期对齐，
想漂移都没有机会。

## 2. ClaimRun：一条 SQL 里的并发艺术

```sql
UPDATE runs SET status='RUNNING', lease_owner=$1,
       lease_epoch = lease_epoch + 1, lease_expires_at = now() + interval '30 seconds', ...
WHERE id = (
    SELECT id FROM runs
    WHERE status IN ('QUEUED','RECOVERING')
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, case_id, ...;
```

### 2.1 为什么必须单条语句？

两步写法（先 SELECT 挑一行，再 UPDATE）在并发下有经典竞态：worker A 和 B 同时
SELECT 到同一行，都去 UPDATE——**同一个任务被领走两次**。单条 UPDATE..WHERE id=
(SELECT ... FOR UPDATE SKIP LOCKED) 让"选行+加锁+改状态"在一条语句里原子完成，
数据库的行锁就是裁判。

### 2.2 SKIP LOCKED 的语义：不排队，绕道走

多个 worker 同时领活：`FOR UPDATE` 说"我要锁这行"，`SKIP LOCKED` 说
"**被别人锁住的行我不等，直接看下一行**"。效果：4 个 worker 并发领取，各拿各的
QUEUED 任务，零互等、零重复。对比另两个选项：
- 无锁：必然重复领取（上述竞态）；
- `FOR UPDATE` 不带 SKIP：worker 之间互相阻塞，退化成串行。

### 2.3 `lease_epoch = lease_epoch + 1`：防僵尸的关键一招

每次交接（含 RECOVERING 被接管）纪元 +1。场景 E 排演：worker A 拿到 epoch=5 后
网络分区卡死 → 租约到期 → worker B 领走，epoch=6 → A 恢复，拿着 epoch=5 来提交
→ 所有写回 SQL 的 WHERE 都带 `lease_epoch=$n` → 0 行命中 → 拒绝（AC24）。
**自增纪元 + 写回验纪元**，一对组合拳。

### 2.4 没活干 = `ErrNoRows`

无可领取行时子查询返回 NULL，UPDATE 影响 0 行，`:one` 得到 pgx 的 `ErrNoRows`。
把"库里的正常状态"（队列空了）表达为"查询的正常失败"，worker 层据此休眠轮询——
不引入任何额外信号表或 NOTIFY（M1 够用，PRD §27 反对预先复杂化）。

## 3. 心跳与提交：影响行数就是协议语言

```sql
-- HeartbeatRun :execrows
UPDATE runs SET lease_expires_at = now() + interval '30 seconds'
WHERE id=$1 AND lease_owner=$2 AND lease_epoch=$3;
```

PostgreSQL 里 `UPDATE` 命中 0 行**不是错误**——协议恰好利用这一点：
`:execrows` 返回影响行数，0 = 你已失权（租约被服务器易主/纪元已变）。
心跳方拿到 0 必须**立刻停手**（PRD §11.3），不再发起新的模型调用。

提交侧同理（CompleteRun/FailRun 的 WHERE 带 owner+epoch 双重身份）：
0 行 = 旧纪元僵尸试图写回，被无声拒绝。

## 4. 幂等回执：INSERT ON CONFLICT 的两段式

```sql
INSERT INTO run_commits (..., payload_hash, ...)
VALUES (...)
ON CONFLICT (run_id, commit_id) DO NOTHING
RETURNING payload_hash;
```

- 首次提交：插入成功，回执在案，返回 payload_hash（AC23 原子提交）；
- **同 ID 同 hash 重放**（网络抖动后重试）：DO NOTHING，无返回行 → Go 侧再查一次
  现有 hash 比对，相同 → 当成功，回显（AC25 幂等）；
- **同 ID 不同 hash**：比对不同 → 409 冲突（AC26）。

为什么拆两步而不是 SQL 里 CASE 完事？**SQL 保持笨，逻辑放 Go**——Go 里的分支
能单测、能断点、能读懂；SQL 里的 CASE 联动应用逻辑只会在半夜的慢查询日志里见。

## 5. 生成代码的两个注意点

1. **可空列 → `*string`**：`lease_owner` 列可空（QUEUED 时没人持有），ClaimRun 的
   参数便生成 `*string`。协议上领取必带 owner，所以 T008 的包装层会把它收窄回
   `string`——**生成的宽类型是数据库事实，包装的窄类型是协议约定**。
2. **`:one` vs `:execrows` vs `:many`** 注解是 sqlc 的"函数签名语言"：
   要一行（ErrNoRows 可能）用 `:one`，要影响行数用 `:execrows`，要列表用 `:many`。

## 6. 测试基建：`setupQueries` 的清场模式

集成测试第一行永远是"清空业务表（保留迁移账本）"——**每个测试都是全新现场**，
测试顺序无关、可任意重跑。T006 翻过的车（残留状态污染）在这里制度化了：
清场进 setup，而不是事后 cleanup 补救。

## 7. 自测证据

| 场景 | 结果 |
| --- | --- |
| `sqlc generate` | 5 个文件生成，0 报错（SQL 与 schema 编译期对齐）✅ |
| 空库领取 | 无行返回 ✅ |
| 种 QUEUED → 领取 | 返回 run-x，epoch=1，状态 RUNNING ✅ |
| 领取后再领取 | 无行（队列已空）✅ |
| 全套 store 测试（含 T006） | ok ✅ |

## 8. 下一站预告

T008 Store 封装：把这些裸查询包进 `WithTx` 事务助手与错误语义映射——
webhook 的"一个 HTTP 请求一个事务"即将登场。
