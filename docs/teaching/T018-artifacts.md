# T018 教学笔记：产物落库——content-addressable 与「每 case 一个 current 草稿」

> 对应文件：`internal/controller/artifacts.go`、`queries/{artifacts,reply_drafts}.sql`、
> `execute.go` 接线（7.5 步）、`artifacts_test.go`
> 面试视角：**内容寻址存储**（git objects 同款设计）+ 状态字段的不变量维护 +
> 故障顺序选择（先文件后库）。

## 1. content-addressable：为什么文件名是 sha256

```
data/artifacts/3f/3fa1…（sha256 前两位分片）/
```

三个性质一次到位：
- **去重**：同内容 = 同哈希 = 同文件。两个 run 引用同一份 README 时磁盘只有一份；
- **完整性自证**：文件名即校验和，任何字节损坏都能被 `sha256(file) != filename` 发现；
- **不可变**：内容变 = 名字变，不存在"覆盖了一份被引用的文件"这类事故。

分片（前 2 位做子目录）防止单目录文件数爆炸——git objects、Nix store、
Docker layers 全是这一个模式。**表里只存引用与哈希**（PRD §18.1），
大内容不进数据库。

## 2. 故障顺序：先文件，后库行

`write()` 的顺序选择：先写磁盘、再插 artifacts 行。

- 文件成功 + DB 失败 → 留下**孤儿文件**：无害（content-addressable 天然幂等，
  下次同内容直接命中），最坏情况是多占几 KB 磁盘；
- 反过来（先插行后写文件）→ 可能留下**库里有引用、盘上无字节**的坏状态：
  前端点开证据引用 404，且没有自愈路径。

原则：**先写"可能留垃圾"的一侧，后写"可能留损坏"的一侧**。垃圾可清扫，
损坏要修复。

## 3. reply_drafts 的不变量与 supersede 事务

业务规则：**每个 case 恰好一个 current 草稿**。维护方式：

```sql
-- SupersedeCaseDrafts + InsertReplyDraft 在同一个事务里
UPDATE reply_drafts SET status='superseded'
WHERE status='current' AND id IN (…经过 runs 关联到同 case…);
INSERT INTO reply_drafts (…, status 默认 'current');
```

两条语句分开执行会有一瞬"零个或两个 current"——审批流会拿到错误草稿。
同 case 跨 run 的关联要经过 runs 表 join（draft 没有 case 列），
这是规范化带来的小代价。

测试断言了完整闭环：第一次分析 → 1 current；第二次分析（reopened 触发的新 run）
→ 旧翻 superseded + 新 current 且属于第二个 run。

## 4. body_digest：审批绑定的内容锚（AC33）

`body_digest = sha256(草稿正文)`。US2 的审批包会绑定它：批准时重比对，
内容变过即失效。它和 artifacts.content_digest 是**同一个值**（正文 artifact
的哈希即正文哈希）——一份哈希两处引用，不存在不一致空间。

NEEDS_INFO 草稿的"正文"是追问列表的 JSON——审批永远不会绑 NEEDS_INFO
（没有可发布的内容），所以这里的 digest 语义无副作用。

## 5. 执行器里的取舍：产物失败不毁掉分析

execute.go 的 7.5 步：`SaveDraft` 失败只记日志、继续提交——因为
`run_commits.result` 里已有完整 JSON（取证兜底），产物缺席只是展示层损失；
为此毁掉一次花了真金白银的分析不值得。但落库失败的草稿不会出现在审批流
（查不到 current 草稿）——**降级不产生错误状态**。

三个 artifact 各自的角色：`reply_draft`（正文，审批对象）、`evidence_pack`
（引用列表，前端证据面板）、`raw_model_io`（模型原始输出，审计与复现）。

## 6. Go 语法/工程点

- `errors.Is(err, store.ErrNoRows)` 判「digest 已存在」——ON CONFLICT DO NOTHING
  的空返回（T011 同款陷阱）；
- `os.Stat` 判存在再写 + `MkdirAll(filepath.Dir(path))` 两级目录一次建齐；
- 文件权限 `0o644`：产物非敏感（草稿与证据），但保持最小权限纪律。

## 7. 面试自问自答

- Q: 为什么不用数据库大字段存正文？ A: PRD §18.1 明确大内容出库——TOAST
  膨胀、备份变慢、流式读取困难；文件系统天生适合不可变大对象。
- Q: 孤儿文件会无限累积吗？ A: 会，但有界且无害；需要时按目录时间清扫即可
  （引用在库里，清扫脚本比对 digest 集合即可安全删除）。
- Q: 用户批准了旧草稿怎么办？ A: superseded 草稿的 approval 包会在预核时
  发现 body_digest 与 current 不一致而失效（T022）——审批永远只能绑 current。

## 8. 自测证据

`go test ./...` 全绿；TestArtifacts 断言：3 个 artifact 行、文件在盘且大小一致、
两轮分析后 superseded/current 各恰一条且 current 属最新 run。
