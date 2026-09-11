# T020 教学笔记：审批包生成——把「人工批准」变成可校验的绑定

> 对应文件：`internal/controller/approval.go`、`queries/approval_bundles.sql`
> 面试视角：**宪法 II（人工审批）的数据结构化**——批准的对象不是"这个任务"，
> 而是「目标 × 内容 × 时限」三元组的精确绑定。

## 1. 审批包 = 一个不可伪造的承诺

```
approval_bundles:
  target          → {repo_numeric_id, issue_number}   批准发到哪儿
  content_digest  → = 草稿 body_digest                 批准发什么（内容锚）
  expires_at      → created_at + 24h                   批准多久内有效
  idempotency_key → bundle-<case>-<digest 前 16 位>     重复操作回显（AC35）
```

任何一环漂移——草稿被 supersede、issue 换了、过了 24 小时——这个包就失效，
必须重新走「分析 → 草稿 → 审批」。**把安全属性编码进数据结构**，
比在流程里加"检查步骤"可靠：检查会漏，结构不会。

## 2. 幂等键第三次登场：还是结构派生

```go
key := fmt.Sprintf("bundle-%s-%s", caseID, draft.BodyDigest[:16])
```

T013 的 `commit_id = stage + run`、T019 的回执、本任务的 bundle key——
同一个设计原则贯穿全项目：**幂等键从业务结构派生，不从随机源生成**。
同一草稿重复点"创建审批包"返回同一个包（测试验证了 ID 相同），
AC35 的"重复批准回显原结果"从创建这一步就开始生效。

## 3. 为什么 target 用 repo_numeric_id 而不是 owner/name

owner/name 可改名（改名后审批包指向一个不存在的仓库）；数字 ID 是 GitHub
分配的稳定主键，与 repositories 表的锚点一致。**凡跨系统的引用，用对方
不变的主键，不用可变的别名**——外键设计的通用法则。

## 4. 实战坑：JSON 大整数的 float64 精度

测试断言 target 时踩到：repo 数字 ID 若是纳秒量级（> 2^53），
`map[string]any` 解码走 float64，精度丢失导致断言失败。两层修正：
1. **测试侧**：`json.Decoder.UseNumber()` 解出 `json.Number`（字符串化的数字），
   `Int64()` 精确比较；
2. **夹具侧**：SeedRun 的 ID 从 `UnixNano()` 改为递增计数器（现实量级）——
   **测试数据不应当制造出现实中不存在的问题量级**，但这条坑本身是真实的：
   JS 前端消费超过 2^53 的 JSON 数字必然丢精度（GitHub API 对大型 ID 用字符串
   返回正是这个原因）。US3 前端展示 ID 时要记住。

## 5. 校验顺序的读法

`CreateBundle` 的四步查询各有一次提前返回：无草稿 → `ErrNoDraft`；
NEEDS_INFO → `ErrNotApprovable`（没有可发布内容，审批无意义）。
错误在**最廉价的检查点**提前失败——先查草稿（一次索引读）再查仓库（joins），
失败的请求绝不拖到最后一层才发现不可批。

## 6. 面试自问自答

- Q: 为什么批准前不校验 target 里的仓库还存在？ A: 这是 T022 发布前重核
  （preflight）的职责——审批时校验是"批准的完整性"，发布时校验是"执行的
  安全性"，两层检查时间点不同、职责不同。
- Q: 24 小时从创建算还是从批准算？ A: 从创建算（PRD §15.2）——审批包是
  草稿的快照承诺，草稿本身会随新分析过时，窗口从绑定生效即起算。

## 7. 自测证据

三个子测试：绑定三元组（target/digest/24h，期望值从库读取而非写死）、
重复创建幂等同包、NEEDS_INFO 不可审批。全套测试绿。
