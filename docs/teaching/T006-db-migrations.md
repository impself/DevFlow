# T006 教学笔记：数据库迁移 V1——业务状态的家地基

> 对应文件：`internal/store/migrations/0001_init.sql`（11 张表）、`internal/store/migrate.go`（迁移器）、
> `internal/store/migrations_test.go`
> 一句话：给 DevFlow 的"唯一权威事实"（宪法 IV）浇筑地基——11 张表 + 状态机 CHECK + 
> 幂等迁移器，从此所有业务状态有了住址。

## 1. 为什么要自己写迁移器（而不是引 golang-migrate）？

迁移这件事的本质只有三句话：**按序执行 SQL、记账防重跑、失败整体回滚**。
golang-migrate 是好工具，但带来 CLI、版本对齐、up/down 语义等一整套概念。M1 的迁移
体量（V1 一版，往后按里程碑加）用 60 行 Go + `embed` 就说清楚了——**协议自己写的
好处是：租约测试可以像调函数一样调迁移**（T019 直接受益）。

### `go:embed`：把 SQL 文件焊进二进制

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS
```

编译期把 SQL 文件打包进可执行文件——部署时一个 exe 就是完整服务，不依赖"迁移文件
在不在部署机上"。注意语法陷阱：`//go:embed` 指令要求变量类型是 `embed.FS`（且必须
import `embed` 包），写成 `fs.FS` 接口类型编译器会直接拒绝——接口是运行时鸭子，
embed 需要具体类型。

## 2. 表设计的四个"宪法级"决定

### 2.1 主键用 text、无业务含义

`id text PRIMARY KEY`，由应用侧生成（nanoid/ULID 风格）。反例：用 `repo_numeric_id`
当主键看似省事，但 GitHub 数字 ID 的"业务含义"是别人的系统给的——万一要支持
GitLab 呢？**主键唯一职责是标识行，不携带语义**。代价是库里看不到生成规则，
换来的是永不迁移主键策略。

### 2.2 循环外键的手术：先建表，后补刀

`inbox_events.run_id → runs.id`，而 `runs.trigger_event_id → inbox_events.id`。
建表顺序无解（鸡生蛋）。SQL 的解法是**后置约束**：

```sql
CREATE TABLE inbox_events (..., run_id text, ...);   -- 先不带 FK
CREATE TABLE runs (..., trigger_event_id text NOT NULL REFERENCES inbox_events(id), ...);
ALTER TABLE inbox_events
    ADD CONSTRAINT inbox_events_run_fk FOREIGN KEY (run_id) REFERENCES runs(id);
```

这是数据库版本的原则性问题：**约束一个不少，只是宣布的时机有先后**。

### 2.3 部分索引：给"找活干"的查询开专用通道

```sql
CREATE INDEX runs_claimable_idx ON runs (created_at)
    WHERE status IN ('QUEUED', 'RECOVERING');
```

两个精妙点：
- **部分索引（WHERE 子句）**：只索引"可领取"的行。一年后 runs 表有一千万行历史，
  这个索引里永远只有几行待办——体积不随历史增长；
- **索引列的小叛逆**：data-model.md 写的是索引 `(status)`，但领取 SQL 是
  `WHERE status IN(...) ORDER BY created_at LIMIT 1`——部分索引已把状态过滤"烧进"
  索引定义里，剩下的排序需要 `(created_at)` 才能走 index scan。照抄文档会得到一个
  用不上的索引。**文档是意图，SQL 是实现，冲突时在代码注释里说明偏离理由**（本文件
  和教学笔记都记了）。

### 2.4 CHECK 约束：状态机的最后一道闸

```sql
status text NOT NULL DEFAULT 'QUEUED'
    CHECK (status IN ('QUEUED','RUNNING','RECOVERING','CANCEL_REQUESTED',
                      'CANCELLED','COMPLETED','FAILED')),
cost_cny numeric(12,4) NOT NULL CHECK (cost_cny > 0),
```

应用代码会有 bug，但数据库是**唯一权威事实**（宪法 IV）——`DREAMING` 这种状态
不该有第二条路写进去。`cost_cny > 0` 直接把 AC47（"费用不得记零"）翻译成了存储层
不变量：估算不出也要给非零估计值 + `estimated` 标记。

## 3. 迁移器的幂等设计：账本模式

```
schema_migrations（账本表）
  0001_init.sql  ✔ 2026-09-10
  0002_xxx.sql   （未来）
```

流程：建账本 → 列目录按文件名排序 → 逐条查账（记过就跳过）→ 未记录的在**独立事务**里
执行 SQL + 记账。两个"为什么"：
- **为什么每版迁移独立事务**：V1 建表成功但记账失败时，回滚到"什么都没发生"，
  下次重跑整个 V1——绝不存在"建了一半的表"这种薛定谔状态；
- **为什么文件名即版本**：`sort.Strings` 让 `0001 < 0002`，人类可读，git diff 友好，
  不需要额外的序号管理。

Checkpoint 要求的"迁移可重复执行"= 第二遍全跳过，测试里连跑两遍验证。

## 4. 测试翻车的两课（真实翻车记录）

### 4.1 `t.Cleanup` 的执行时机

第一版把夹具 run1 的清理挂在"费用"子测试里，下一个"状态机"子测试去 UPDATE run1
时**行已被删**——`UPDATE` 影响 0 行，不触发任何 CHECK，也不报错，测试误判"约束失效"。
两个教训：
- `t.Cleanup` 在**注册它的那个 subtest 结束时**执行，不是整个测试函数结束时；
- `UPDATE ... WHERE id=X` 撞上"行不存在"会**静默成功**——断言约束时必须确保被测行
  真实存在（或者检查 `RowsAffected`）。

### 4.2 残留状态跨运行传染

第一版 delivery_id 子测试没有清理，上一轮失败留下的 ev1 行让下一轮"首次插入"报
唯一冲突——**测试自己的垃圾污染了自己的现场**。修法：每个子测试自带 `t.Cleanup`
按外键依赖逆序删夹具，测试从此可任意重跑。

> 集成测试的通用纪律：`TEST_DATABASE_URL` 未设置就 `t.Skip`——单元环境没有 PG
> 也能跑全套其他测试，CI 和本地互不绑架。

## 5. 自测证据

| 场景 | 结果 |
| --- | --- |
| 双跑迁移（幂等） | PASS，11 张表齐全 ✅ |
| delivery_id 唯一 | 重复插入被拒（23505）✅ |
| cost_cny=0 | CHECK 拒绝 ✅ |
| 非法 run 状态 | CHECK 拒绝 ✅ |
| 连续 4 轮 `go test -count=1` | 稳定绿 ✅ |
| PG 18 容器（compose 修复后） | healthy ✅ |

## 6. 下一站预告

T007 用 sqlc 把"领取 run、心跳续租、幂等提交"写成 SQL 查询集——租约协议的心脏
即将开始跳动。
