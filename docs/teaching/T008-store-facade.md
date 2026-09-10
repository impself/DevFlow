# T008 教学笔记：Store 封装——持久化的唯一门面

> 对应文件：`internal/store/store.go`（Store 门面 + WithTx + 错误语义映射）、
> 测试基建加固（`migrations_test.go` / `store_test.go`）
> 一句话：给数据库装一扇"唯一的大门"——事务边界写成函数作用域，数据库方言
> 翻译成业务错误，controller/publisher 以后只认这一个门面。

## 1. 门面模式：为什么 controller 不许直接摸 pgxpool

```go
type Store struct {
    pool *pgxpool.Pool
    *db.Queries   // 内嵌：sqlc 的查询方法直接提升到 Store 上
}
```

三个收益：
1. **依赖收敛**：将来换驱动、加只读副本、改池参数，只动这个包；
2. **结构体嵌入（embedding）的复用**：`*db.Queries` 内嵌后，`store.UpsertCase(...)`
   直接可调——组合优于继承的 Go 版表达，一行获得全部类型安全查询；
3. **可测试性**：任何组件拿到 `*Store` 就有了全部持久化能力，构造函数注入即可 mock。

代价（诚实地说）：Store 会随任务膨胀（每个 US 加几个方法）。对策是**方法按域分文件**
（webhook 域、审批域各一个文件），门面本身只留装配与协议代码。

## 2. WithTx：把事务边界变成函数作用域

```go
func (s *Store) WithTx(ctx context.Context, fn func(q *db.Queries) error) error {
    tx, err := s.pool.Begin(ctx)
    ...
    defer tx.Rollback(ctx)          // 保险丝
    if err := fn(s.Queries.WithTx(tx)); err != nil {
        return err                   // 失败：defer 回滚
    }
    return tx.Commit(ctx)            // 成功：显式提交
}
```

### 2.1 为什么用闭包而不是让调用方自己 Begin/Commit？

事务的本质纪律是"**要么全有，要么全无**"，Begin 和 Commit 分散在调用方手里时，
忘记提交、忘记回滚、提前 return 绕过提交——每种错法都真实存在。闭包把事务压进
函数作用域：**fn 正常返回 = 提交，fn 返回 error = 回滚**，物理上写不出"忘了提交"。

webhook 的场景（T011）正是为它准备的：登记 inbox_event + 建 case + 建 run 三个写
必须同生共死——一个 HTTP 请求一个 WithTx。

### 2.2 `defer tx.Rollback(ctx)` 的双重身份

提交之后再 Rollback 是 no-op（pgx 返回 ErrTxClosed，忽略即可），所以这行 defer 的
语义是"**未提交就回滚**"的保险丝——包括 fn panic 的极端路径。注释里专门写了
"多这一次调用是保险不是 bug"，因为 code review 时它最常被误删。

### 2.3 `Queries.WithTx(tx)`：sqlc 的事务姿势

sqlc 生成的 `Queries` 绑定一个执行器（池或事务）。`WithTx` 返回一个"跑在事务上"
的临时 Queries——同一个类型，两条执行通路。生成代码的设计把"业务查询"和"在哪个
上下文执行"解耦了，这是 sqlc 比 ORM 轻的关键之一。

## 3. 错误语义映射：数据库方言不出边界层

```go
func IsUniqueViolation(err error, constraint string) bool {
    var pgErr *pgconn.PgError
    if !errors.As(err, &pgErr) || pgErr.Code != "23505" { return false }
    return constraint == "" || pgErr.ConstraintName == constraint
}
```

调用方问的是"**是不是重复投递**"，不是"SQLSTATE 是不是 23505"。把方言翻译钉在
store 边界，换数据库（哪怕只是换驱动版本改了错误类型）时翻译层只有一处。

- `errors.As` 沿错误链找 `*pgconn.PgError`——pgx 的错误常被包装（fmt.Errorf %w），
  直接类型断言会在包装层断掉（T002 教学笔记讲过 `==` 之坑，`As` 是它的同胞兄弟）；
- **约束名是合同**：`cases_issue_id_key` 是 PostgreSQL 的自动命名规则（表_列_key），
  测试里显式断言约束名，防止"某个唯一冲突"张冠李戴成"我要的那个冲突"。

## 4. 测试翻车续集：夹具的生命周期归属谁

T008 的测试第一轮又红了，这次的教训比 T006 更深一层：

1. **子测试级 seed + 测试级清理不匹配**：三个子测试都 seed 同一行 `r-tx`，但清理挂在
   测试级——第二个子测试 seed 时撞主键。修法：`seedRepo` 把 case 行的清理注册在
   **subtest 自己的** `t.Cleanup` 上，夹具跟着注册它的作用域消亡；
2. **`ON CONFLICT DO NOTHING` 会把"撞了不该撞的约束"也吞掉**：seed 仓库用
   DO NOTHING 后，残留行导致插入静默跳过，下一个 insert 报外键错误——真正的病灶
   （主键残留）却被外键报错误导。修法：seed 保持 DO NOTHING（幂等），但**清理要
   按依赖逆序删干净**（先 case 后 repository）；
3. **测试级清理删不掉被引用的行**：`DELETE repositories` 因 case 外键引用而静默失败
   （错误被忽略），残留跨运行传染。修法完成后连跑两轮稳定绿。

一句话沉淀：**集成测试的夹具要像栈一样进出——谁 seed 谁（的作用域）负责清，
清理顺序永远与依赖方向相反。**

## 5. 自测证据

| 场景 | 结果 |
| --- | --- |
| `go build / vet` | ✅ |
| WithTx 提交路径 | case 落库可查 ✅ |
| WithTx 回滚路径 | boom 原样上抛，case 无痕 ✅ |
| 唯一冲突语义判断 | 识别 `cases_issue_id_key`，不误判他约束 ✅ |
| 连续两轮全套 store 测试 | ok / ok ✅ |

## 6. 下一站预告

T009 GitHub 接入层：ghinstallation 的 App 身份、go-github 客户端工厂、webhook 验签。
控制层即将第一次"看见" GitHub。
