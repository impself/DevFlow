# 宪法合规自查记录（T033）

> 自查日期：2026-09-11 ｜ 范围：M1 实现代码与配置（T001–T029）
> 方法：逐原则 grep 取证 + 对照实现文件；T032 端到端回归补运行时证据（待凭据）。

| 原则 | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| **I 自动化红线** | 对外动作仅 `POST_ISSUE_COMMENT`，无合并/分支写入/强推 | ✅ | 写 GitHub 的调用面全仓唯一：`internal/github/resolver.go:90` `Issues.CreateComment`；无任何 PullRequest/Merge/Contents-write 调用（grep 取证）；schema 层 `action_type` enum 单值（T025 测试） |
| **II 人工审批** | 副作用发布前必须过审批包；审批绑定目标×内容×24h；服务端认证 | ✅ | `Publisher.Publish` 只接受 APPROVED（条件 UPDATE 抢占）；`approval.go` 三元组绑定 + AC33 批准前 digest 核对 + T022 preflight 执行时复验；AC32 `OperatorAuth` 服务端令牌；重复批准幂等回显（T021 测试） |
| **III 最小权限与隔离** | 凭据只在 Go 侧；Python 无 DB 凭据；不抓外链 | ✅ | Python 仅持 `DASHSCOPE_API_KEY` + `DEVFLOW_INTERNAL_TOKEN`（healthz 只报布尔）；`DATABASE_URL` 无 Python 消费点；证据只允许来自固定 SHA 预取文件（逐字门强制） |
| **IV 合同优先** | contracts/ 唯一真源，双端校验，业务数据仅 Go 写 | ✅ | schema 内嵌 Go（T014 embed + 复验）、Python 镜像 + 双向测试（T015/T025）；`internal/store` 是唯一 DB 写入路径，Python 无连接 |
| **V 证据驱动** | quickstart 映射 AC；未核实事实显式标注 | ✅ | quickstart.md 场景 A–F ↔ AC 对照表存在；research.md 单价等标注「未核实」；本自查留档 |
| **VI 持久幂等** | delivery 去重、SKIP LOCKED 租约、epoch fencing、commit 幂等回执 | ✅ | `inbox_events.delivery_id` 唯一（T011 测试）；领取/心跳/提交 SQL 带 epoch 守卫（T019/T013 测试）；`(run_id, commit_id)` 主键 + hash 比对 |
| **VII 范围纪律** | 仅 M1 范围；预算参数化并记录 | ✅ | 无补丁/DAG/分支发布代码；预算常量集中（worker.go/execute.go：24 次/900s/30s 租约）与 PRD §21.5 对齐；model_calls 逐调用记账（不得记零，DB CHECK） |
| **VIII 复用优先** | 先调研后自研；license 核实 | ✅ | research.md 三路调研 + 两轮实现期调研（research-alignment.md 归档）；依赖 license：Gin/MIT、pgx/MIT、sqlc/MIT、go-github/BSD-3、ghinstallation/Apache-2.0、agentscope/Apache-2.0、jsonschema/Apache-2.0 |

## 发现与处置

1. ~~发布端点未接线~~（review P0）——已修复并纳入 `writeActionResult` 统一映射；
2. ~~`[bot]` 名字匹配可漏 App 账号~~（调研 P0）——已升级 `sender.type`；
3. ~~引用"可核对"未自动化~~（调研 P0）——已上 `gateEvidence` 逐字门；
4. 已知取舍（记录不阻塞）：issues_write 能力无法无损实测（以 App 权限配置为准，
   Checkpoint 场景 B 真实发布验证）；成本 token 缺失时估算而非熔断（M1 单操作者可控）。

## 结论

八项原则全部通过；T032 端到端回归（需 DASHSCOPE_API_KEY + GitHub App）完成后
本记录补运行时证据列。
