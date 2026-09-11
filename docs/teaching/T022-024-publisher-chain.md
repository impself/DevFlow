# T022–T024 教学笔记：发布链——外部副作用的安全发布（面试重点篇）

> 对应文件：`internal/publisher/{publisher,preflight,comment}.go`、`internal/github/resolver.go`、
> `queries/actions.sql`、cmd/api 端点
> 面试视角：**这是全项目最能讲的部分**——「向不可撤回的外部世界发副作用」的
> 分布式一致性。三个 AC（36/37/FR-8）+ 两轮 review 修复 + 业界调研对照。

## 1. 核心问题：一条发出去的评论收不回来

CreateComment 成功但响应丢了（超时/断网）——你不知道它成没成。此时只有两个
错误选择和两个正确选择：

- ❌ 盲重发：可能发出第二条（GitHub 评论 API **无幂等键**，已核实）
- ❌ 当失败：评论已发出但系统标记 FAILED，状态与现实分裂
- ✅ **核对（reconciliation）**：按「目标 + bot 身份 + 内容全文」拉远端评论，
  命中即 SUCCEEDED 补锚点（AC36）
- ✅ 查无实据 → 保持 RECONCILING 转人工——**绝不自动重发**

这是 Stripe 对外部非幂等 API 的标准立场（见 research-alignment.md §1.5），
理论根基是两将军问题。面试金句：*「exactly-once 是幻觉，你能选的只有
at-least-once + 接收方去重，或者 at-most-once + 人工对账——我选后者，因为
重复的评论比漏发的评论更难看。」*

## 2. 错误分类学：isDefinitive 的两条线

```
4xx（除 408/429）  → GitHub 明确拒绝，请求未生效 → FAILED（终态）
408/429/网络/5xx   → 结果不确定（可能已生效）   → RECONCILING（核对）
```

review 抓过两个真 bug：
- **P1-4**：初版 `400≤code<500` 把 **429 限流**也判死刑——一次限流杀死一个
  已批准的 bundle。限流意味着「请求未被处理、无副作用」，属可重试临时态；
- **P1-5**：preflight 的**基础设施错误**（网络断/5xx）被当成「核实了且不满足」
  而作废审批。修正为错误二分：违反约束（哨兵）→ 作废；**没核实成** →
  `ErrPreflightInfra`，action 回退 PENDING 可重试——误杀审批和漏发一样是事故。

这引出一个通用原则：**「失败」必须区分「确定性失败」与「结果未知」，
两者的处理路径完全不同**。Distinguish them at the lowest layer that can tell them apart（HTTP 状态码层）。

## 3. 状态机的恢复语义：EXECUTING 不是终态（review P1-1）

发布链有六个状态跃迁，每个都是条件 UPDATE（0 行=别人已推进）：

```
bundle: APPROVED → EXECUTING → EXECUTED / EXPIRED
action: PENDING → EXECUTING → SUCCEEDED / FAILED / RECONCILING
```

关键设计：**重入 Publish 时不假设 EXECUTING 是「正在被别人执行」**，而是按
action 状态续办（resume + dispatchAction）：
- 无 action / PENDING → 崩溃发生在触碰 GitHub 前 → **继续执行**；
- EXECUTING → 崩溃可能发生在 CreateComment 中途，结果未知 → **先核对**；
- RECONCILING → 核对；SUCCEEDED/FAILED → 回显。

这消除了三个卡死窗口（抢占后崩、action 建后崩、发布中崩）。配套修复：
infra 错误把 action 回退 PENDING（我们知道没碰过远端），真崩溃留 EXECUTING
走核对——**「执行过没有」是可观测的状态，不是猜测**。

## 4. preflight 的三个工程细节（FR-8）

1. **服务端过滤代替客户端翻页**（review P1-3）：GitHub 列评论默认**最旧 100 条
   在第 1 页**——批准后的新人类回复恰好在末尾，客户端逐页判断必漏。改用
   `Since: bundle创建时间` 让 GitHub 服务端过滤，分页问题整体消失；
2. **时钟安全余量**：GitHub 评论时间戳秒级、DB 微秒级、两端服务器有时钟偏差
   ——Since 前拨 2s，边界评论宁可误判为「新」（保守方向：多拦一次重发的
   代价只是人工再批）；
3. **bot 豁免**：`[bot]` 后缀是 GitHub 命名 bot 的约定（用户名不允许方括号），
   我们自己的追问评论不该触发「有人类插话」。

## 5. Review 工作流的战果（这次审查的价值）

- **P0**：发布端点没接线（`s.publisher` nil → 必 panic）——我的字符串补丁静默
  失败，编译器救不了「字段加了没赋值」。教训：**装配代码的改动要靠 HTTP 层
  测试兜底，go build 通过≠接线正确**；
- P1×6 全修（恢复语义/再核入口/分页/429/infra 二分/错误映射）；
- P2×5（UNIQUE(bundle_id) 库级保证 1:1、failDefinitive 同步 EXPIRED 不留僵尸、
  测试补哨兵覆盖）。

## 6. 自测证据（10 个场景）

正常发布（SUCCEEDED+锚点+EXECUTED）、重复发布回显零重发、Issue 关闭拦截、
响应丢失核对命中、查无实据转人工、4xx 判死、新人类回复拦截（Since 过滤）、
infra 不作废可重试、429 不判死、中断恢复核对命中。全套 `go test ./...` 绿。

## 7. 面试自问自答

- Q: 为什么不在评论正文里嵌唯一标记（如 `<!-- action_id -->`）辅助核对？
  A: 正文是审批绑定的内容（digest 锚），加标记=发布的内容与批准的内容不一致，
  违反宪法 II 的绑定语义。改用 [bot] 账号限定 + 全文比对，误命中概率足够低。
- Q: RECONCILING 永远没人触发怎么办？ A: 操作者 API 有 reconcile 端点
  （review P1-2），且 GitHub 读延迟下查无实据不是永久结论——观察窗后人工再核。
- Q: 为什么 bundle 和 action 两层状态机？ A: 职责分离——bundle 是「审批承诺」
  的生命周期（宪法 II 域），action 是「外部执行」的生命周期（分布式一致性域）；
  1:1 由 UNIQUE 索引库级保证。审批过期作废不影响核对进行，反之亦然。
