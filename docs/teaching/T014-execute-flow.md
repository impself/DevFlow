# T014 教学笔记：Run 执行流——从领取到原子提交的完整编排

> 对应文件：`internal/controller/execute.go`、`internal/contract/`（schema 校验）、
> `internal/runtimeclient/`（出站客户端）、`internal/github/contents.go`（预取）、runner 接线
> 面试视角：**信任边界设计**（合同复验）+ **确定性幂等键**（结构派生）+
> 预算护栏的位置——三段都是系统设计面试的黄金素材。

## 1. 执行流的八步编排（每步一个失败归宿）

```
预算检查 → 读 case/repo → 解析快照 → 预取固定 SHA 文件
  → 调智能层(300s 兜底) → 合同复验 → 记费用 → 原子提交(AC23-26)
```

失败归宿设计：步骤 1-7 的任何失败 → **返回 error**，worker 统一走 FAILED
（或上抛给重试路径）；步骤 8 的失败是**协议错误**（ErrStaleEpoch 等），租约保护。
执行器自己不落 FAILED——失败处理集中在 worker 一处，避免两处写终态互相竞态。

## 2. 合同优先（PRD §28.2）：信任边界的落点

**智能层返回的字节不可信**——即使 Python 内部已做 Pydantic 校验（T015/T016），
Go 收到后必须按 schema **再验一遍**。这不是重复劳动，是信任边界：

> 进程内可以信任（同一编译单元），跨语言/跨网络一律不信任。
> Python 侧的校验防「模型输出乱来」，Go 侧的校验防「实现漂移/版本错配」。

实现：`internal/contract` 用 `//go:embed` 内嵌 schema 原文 +
`santhosh-tekuri/jsonschema/v6` 校验——**校验逻辑就是 schema 文件本身**，
条件规则（ANSWER_READY 必须有 reply+evidence、degraded 不得 READY、追问 ≤3）
零重复实现。Go struct 只是反序列化镜像，注释明确「单一事实来源在 schema」。

一个层次纪律的实证：`cost_cny=0` 在 schema 层是**合法的**（minimum: 0），
「不得记零」（AC47）由 model_calls 表的 `CHECK (cost_cny > 0)` 强制。
合同层与存储层各管各的，测试把这条层界固化下来。

## 3. 确定性幂等键：commit_id 从结构派生

```go
commitID := "issue-analysis-" + claim.ID
```

重试同一 run 的同一阶段，commit_id **天然相同**——这就是 AC25 幂等的钥匙。
对比随机 UUID：每次重试生成新 ID，幂等机制完全失效。规则：
**幂等键应从业务结构（run, stage, seq）派生，而不是从随机源生成**。
payload_hash 则是对 result 原始字节的 SHA-256——同 ID 不同内容立刻被
T013 的协议拒绝（409）。

## 4. 固定快照 + 固定 SHA：可复现的分析输入（FR-3）

分析输入的三份原料全部钉死在触发/执行时刻：
- **Issue 内容**：webhook 时的 `input_snapshot`（T011），之后编辑不影响本次执行；
- **仓库内容**：执行开始时取 **head SHA**，文件一律按该 SHA 读取（Contents API
  `ref=sha`）——即使分析过程中仓库有新推送，读到的也是同一版本；
- **策略版本**：快照里的 policy_version。

证据引用（AC03）因此可核对：每条 evidence 带 path+sha+lines，
任何人拿到 run_id 都能回到那个 SHA 验证引用真实性。**可复现性是可审计性的前提**。

预取上限三道闸：≤5 文件、单文件 1MB、总量 2MB——prompt 注入成本是
agent 系统真实的钱，闸门必须在调用之前。

## 5. 预算护栏：三层各管一段

| 层 | 机制 | 挡住什么 |
| --- | --- | --- |
| run 层 | `model_calls_used >= max(24)` → 直接提交 LIMIT_REACHED | 单 run 失控 |
| 调用层 | 智能层调用 300s 兜底 + run 总预算 900s | 慢响应/悬挂 |
| 记账层 | 每次调用后 `model_calls_used+1`（不带 epoch 校验） | 预算漏记 |

细节：预算计数**故意不做 epoch 校验**——僵尸执行的模型调用也真实花了钱，
预算必须如实累计（与终态写回的 fencing 语义相反，两种选择都有明确理由）。

## 6. Go 语法/工程点

- **出站客户端错误分层**：`ErrUnavailable`（503，可重试）与普通错误分开——
  调用方按错误类型决定「重试」还是「失败」，错误是 API 的一部分；
- `pgtype.Numeric` 经字符串 `Scan("0.0120")` 构造——float 直接构造 numeric
  有精度漂移风险，货币字段永远走字符串/Decimal 路径；
- **响应体读取限长**：`io.LimitReader(resp.Body, 8MB)`——对外部服务的任何读取
  都要设上限，失控响应体是内存事故的经典来源；
- go-github 的 `fc.Content == nil` 表示文件超过内联上限（约 1MB），
  与「文件不存在」是两种正常形态，都要跳过而不是报错。

## 7. 测试：替身注入点的选择

执行器依赖两个接口：`RepoContentProvider`（预取）与 `Analyzer`（智能层）——
**接口切在「进程边界」上**，假实现各 10 行，四个场景（全流程/合同违规/
不可用/预算耗尽）全部离线可测、真实 PG 落库断言。接口切错位置（比如按函数
切）会让替身写不出来或脆断。

## 8. 面试自问自答

- Q: 为什么分析和提交不在 Python 侧做？ A: 宪法 III 凭据隔离——Python 不持
  数据库写权限；Go 提交保证租约协议的完整性（Python 看不到 epoch）。
- Q: 智能层 503 为什么要区分？ A: 过载是临时态，重试有意义；合同违规是
  永久态（版本错配），重试无意义。错误分类决定重试策略。
- Q: 如果分析成功但提交时失去租约？ A: ErrStaleEpoch → 事务回滚（不留回执）
  → worker 记日志放弃写回。模型费用已花，但预算记账已先行落库——钱没有白花到
  查无此账。
