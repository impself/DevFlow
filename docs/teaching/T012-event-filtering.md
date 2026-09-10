# T012 教学笔记：事件过滤与业务去重——agent 的"触发面防线"

> 对应文件：`internal/controller/events.go`（+纯函数单测）、`webhook.go` 接线、集成测试
> 面试视角：这是整个项目里**最直接的 agent 工程考点**——触发面控制与防循环。
> 后端考点是纯函数设计与表驱动测试。

## 1. Agent 工程：为什么过滤规则是 agent 系统的生死线

自动 agent 的事故史几乎都发生在"触发面"上：

1. **过度触发**：每次 Issue 编辑都触发一次 LLM 分析 → 费用线性爆炸；
2. **循环触发**（最经典）：bot A 的输出成为 bot B 的输入事件，B 的动作又产生
   事件唤醒 A——两个"勤奋"的 agent 无限互相调用，直到预算烧穿或被人工掐断。
   pr-agent、claude-code-action 都吃过这类亏（research.md §1）；
3. **重放放大**：webhook 重投 + 无去重 → 一次用户操作触发 N 次分析。

M1 的防线是三层，各管一段：

| 层 | 机制 | 挡住什么 |
| --- | --- | --- |
| 订阅层 | GitHub App 只订阅 issues 事件 | comment/PR 类事件根本不进来 |
| 过滤层（本任务） | action 白名单 + bot sender 忽略 | 无意义动作 + bot 循环 |
| 幂等层（T011） | delivery_id 唯一索引 | 重复投递 |

**为什么忽略所有 `[bot]` sender 而不只是自己的 bot**：识别"自己"需要配置 app slug，
而 M1 的 bot 永远不该以 issues 事件形式出现——任何 bot 的 issue 动作都可疑。
防线上宁可过拦，这是一次性的保守换长期的安全。

**循环的数学**：设单次分析成本 c，循环周期 t，无过滤时费用增速 = c/t（指数级如果
每次循环创建新对象）。过滤规则把增速精确截断为 0——这是一行 `HasSuffix(login, "[bot]")`
的价值。

## 2. 后端：纯函数 + 表驱动测试是"规则类代码"的标准答案

```go
func decideIssueEvent(evt *githubpkg.IssuesEvent) rejection
```

过滤规则是**需求最常改**的代码（"reopened 也要处理"、"白名单仓库"……）。
让它腐化的方式是混进副作用（查库、写日志、直接改响应）。解法：

- **输入事实，输出决定**：函数只看事件内容，不碰 DB/HTTP；
- **测试即规格**：9 行表格 = 全部规则组合。规则变更时改一行、补一行测试，
  review diff 一眼可读；
- 副作用（查库判范围、写 ignored 留痕）留在 Handle 的编排层——**决策与执行分离**。

面试表达：规则类代码的复杂度控制手段不是抽象工厂，是"纯函数 + 表驱动测试 +
副作用外移"三件套。

## 3. 语义细节：reopened 为什么新建 run 却复用 case

- **case = Issue 的长生命周期上下文**（同 (repo, issue_number) 唯一），
  `UpsertCase` 命中已有行时只更新 `updated_at`——首标题保留（快照语义）；
- **run = 一次有预算的执行**。用户 reopen 通常意味着"问题回来了，请再看一眼"，
  是新的分析请求，必须有新 run（新快照、新预算、新租约）。

一对多关系（case 1—N runs）让"同一问题的多次分析"天然可追溯——这是任务系统
里"作业（job）与执行（attempt/run）分离"的通用模式，K8s Job/CronJob 同款思想。

## 4. Go 语法点

- `githubpkg.String()` / `GetLogin()`：go-github 的字段全是指针 + getter，
  `Get*` 自动解引用空指针返回零值——**对 API 响应做防御式读取**的惯例
  （外部 JSON 任何字段都可能缺）；
- `strings.HasSuffix` 判 `[bot]` 后缀：GitHub 命名 bot 用户的官方约定
  （`app-slug[bot]`），后缀匹配比 contains 严——防止 slug 中间恰好含 [bot]。

## 5. 自测证据

- 纯函数 9 用例表驱动（opened/reopened/edited/closed/labeled/bot 组合）全过；
- 集成新增 2 子测试：reopened 复用 case 新建 run（1 case 2 runs）、bot 触发 200
  忽略不建 run；全套 `go test ./...` 绿。

## 6. 面试自问自答

- Q: 如果两个 webhook 同一毫秒到达同一 issue？ A: delivery_id 不同（两次真实触发），
  都会建 run——但 SKIP LOCKED 领取保证串行执行；若要"同内容版本去重"可在
  insertEvent 后比对 input_snapshot 哈希（M1 场景 D 只要求投递去重）。
- Q: 防循环为什么不在发布层做？ A: 分层防御——触发层拦住"进来"，发布层（T024）
  还有"内容核对 + 绝不自动重发"。多层各挡一类故障，单一防线是单点故障。
