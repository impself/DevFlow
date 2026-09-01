# DevFlow Constitution
<!-- Sync Impact Report
- Version change: 1.0.0 → 1.1.0（1.0.0 初次批准于 2026-09-01）
- Modified principles: 无（既有 I–VII 未改动）
- Added sections: Core Principle VIII（复用优先与调研先行，操作者 2026-09-01 提出）；
  「开发工作流与质量门槛」新增 plan 阶段先行调研交付要求
- Removed sections: 无
- Follow-up TODOs: 无
-->

本宪法约束 DevFlow 仓库内的一切规格（spec）、计划（plan）、任务（tasks）、实现与文档。
需求最高来源是 `docs/DevFlow 完整 PRD.md`（V3.0）；本宪法从其中提炼不可妥协原则，
与 PRD 冲突时以本宪法为准，并触发治理修订流程。

## Core Principles

### I. 自动化安全红线（NON-NEGOTIABLE）

任何模式下系统 MUST NOT：自动合并 PR、强制推送、直接写默认分支、删除远端分支、
关闭 Issue、部署应用（PRD §2.3、§5.1）。对外 Review 动作首版只允许 COMMENT，
MUST NOT 以维护者身份提交 APPROVE / REQUEST_CHANGES。系统 MUST NOT 提供任何合并入口，
"批准发布"与"合并 PR"在语义上必须分离（PRD §6.3）。

理由：这些操作具有不可逆的外部影响，而模型输出可能错误；红线不允许任何模式解除。

### II. 人工审批授权（NON-NEGOTIABLE）

一切对外写入动作（发布 Issue 评论、发布 Review COMMENT、发布分支、创建 Draft PR）
MUST 先经审批人明确批准。审批人定义为在 DevFlow 中取得该仓库发布权限的自然人操作者；
AI Reviewer 的结论仅是审批参考，MUST NOT 被实现或表述为授权（PRD §0.1、§15）。

外部动作白名单固定为 4 种：`POST_ISSUE_COMMENT`、`POST_REVIEW_COMMENT`、
`PUBLISH_BRANCH`、`CREATE_DRAFT_PR`；新增动作种类属于宪法修订（PRD §5.1）。
审批包（ApprovalBundle）MUST 绑定精确目标与内容摘要，有效期默认 24 小时；
审批后目标、内容、代码树或策略发生任何变化，原批准 MUST 失效（PRD §15.1–15.2）。

TEST_REPO_AUTO 自动发布模式默认关闭；开启时 MUST 满足：显式指定自有测试仓库数字 ID
白名单、仅允许约定动作、分支前缀隔离（如 `devflow/`）、默认 24 小时有效期、
每日最多 5 个发布批次；且测试失败、代码树改变、凭据越权、超预算或保护路径变更时
仍然阻止发布。Issue 作者、模型、仓库 README、Webhook 载荷均 MUST NOT 能修改此模式
（PRD §5.2）。

### III. 最小权限与隔离执行

仓库代码 MUST NOT 在持有 GitHub 凭据、模型密钥、数据库密码或控制面凭据的进程中执行
（PRD §2.3、§12.2）。隔离执行 MUST 满足基线：非 root、无特权与额外能力、仅挂载任务
目录与受控临时目录、测试阶段默认无外网、镜像固定 digest、每任务 2 CPU / 2 GiB 内存 /
256 进程 / 180 秒超时（PRD §12.3）。

执行配置（镜像、依赖、命令、测试入口）由操作者确认后固定；模型 MUST NOT 修改执行
配置、GitHub 工作流、依赖锁文件、发布脚本或安全策略，受保护路径必须在导入、封存、
发布各入口被拒绝，不能只依赖模型自觉（PRD §12.4–12.5、§2.3）。
GitHub 凭据 MUST 按用途申请最小权限并显式缩小 token 范围（PRD §16.1）。

### IV. 合同优先

先明确 Snapshot、NodeResult、PatchManifest（PatchVersion）、TestReport、ApprovalBundle、
ActionReceipt 的 schema，再连接真实模型与 GitHub API；跨服务数据 MUST 经过明确 schema
校验，不允许未验证的字符串拼接（PRD §28.2）。关键合同 MUST 带版本号与兼容说明；
Run 开始后使用固定 schema/策略版本，不兼容变更要么走兼容路径恢复，要么显式停止重跑，
MUST NOT 在反序列化失败后悄悄清空状态。

业务状态的权威来源是 PostgreSQL；Python 与 Runner MUST 通过内部接口提交状态，
不持有数据库写账号（PRD §18.1）。

### V. 证据驱动与诚实表述

每条验收标准 MUST 能从数据库状态、Artifact、受控 GitHub 测试对象或日志中取证
（PRD §25）。涉及远程写入的验收仅在明确授权的自有测试仓库执行。

MUST NOT：把"有测试报告"或"模型自评通过"表述为已证明修复正确；把设计写成既有成果；
把未实测的性能写成指标；在简历或文档中预写 QPS、P95、可用性百分比；
借用 AliGo 等参考项目的准确率、业务规模或收益作为本项目成果（PRD §0、§24.6、§28.6、
§30.3）。框架提供的能力与自研应用工程 MUST 分开表述。

### VI. 持久执行与幂等

跨进程崩溃、重启与网络中断，任务状态 MUST 保持一致：数据库任务领取、心跳与租约
（默认 5 秒心跳 / 30 秒租约，lease_epoch 单调递增）、原子提交（同 commit_id +
payload_hash 幂等返回原回执，同 ID 不同内容返回冲突）、Webhook delivery ID 去重与
业务触发去重分开（PRD §11、§16.3）。

重复投递、迟到结果、旧租约执行者的写回 MUST NOT 推进或污染当前状态。已发出的外部
发布动作不随 Run 取消自动撤销，只能核对其结果（PRD §11.6、§15.6）。

### VII. 范围纪律与预算护栏

P0 > P1 > P2 的优先级 MUST NOT 被任何生成的计划或实现过程改写（PRD §0.3）。
延期时按既定删减顺序缩减范围，最后仍须保住"Issue → 修复 → 验证 → 审批发布"主线；
MUST NOT 通过取消审批、取消隔离或伪造测试通过来满足交付范围（PRD §27.4）。

每次开发按一个真实案例纵向打通各层，不预先一次性建设全部数据结构（PRD §27.3）。
保护参数集中配置并版本化，Agent MUST NOT 在运行时提高限制：同时 2 个 Run、
每 Run 并发 3 节点、单计划 ≤12 节点、单 Run ≤24 次模型调用且 ≤900 秒、
Case 累计 5 Run / 72 次调用 / 6 个补丁版本、单 Job 测试 ≤180 秒（PRD §21.5）。

### VIII. 复用优先与调研先行

启动项目或制定较大技术方案前，MUST 先调研 GitHub 等平台的同类项目、官方文档、
实现思路与源码。存在成熟方案时，MUST 先评估许可证（license）兼容性、维护状态、
安全风险与适配成本，再决定复用、借鉴或自研；可复用内容直接取用，多套开源方案
交叉对比，取其优势、剔除缺陷糟粕。引用第三方成果 MUST 如实标注出处，不得把
复用内容表述为自研（与原则 V 一致）。简单 bug 修复、明确的小改动或离线任务
不强制调研。

理由：杜绝闭门造车与重复造轮子；对已有生态的正确取用与署名，本身就是本项目的
工程可信度组成部分。

## 技术栈与工程约束

- 服务层：Go + Gin（Controller、Runner、Publisher 同仓不同进程）；智能层：
  Python + FastAPI + AgentScope（唯一 Agent 编排层）；前端：React + TypeScript + Vite
  （桌面 Web 优先）；存储：PostgreSQL。首版不引入 Redis/Kafka/Temporal 与向量数据库。
- 代码组织按 PRD §28.1（services/control、services/runtime、web、contracts、evals、
  tests、infra、docs）；`devflow-fixture` 是独立仓库，不与 DevFlow 本体混部。
- 首个修复环境只支持一个固定的 Python ExecutionProfile；Go/TypeScript 仓库列为 P1。
- 评测使用 B01–B20 业务案例（12 开发集 + 8 保留集）与三策略对照；保留集评分材料
  MUST 放在 Agent 无法挂载或索引的位置（PRD §23、§24）。
- 改变目标语言、执行环境、发布权限或支持的副作用类型，MUST 同步修订验收范围并触发
  宪法修订；调整界面样式或字段命名不需要。

## 开发工作流与质量门槛

- 本仓库采用 GitHub Spec Kit 做 spec 驱动开发，工作流为：
  `$speckit-constitution` → `$speckit-specify` → `$speckit-clarify`（可选）→
  `$speckit-plan` → `$speckit-tasks` → `$speckit-analyze`（可选）→ `$speckit-implement`。
- 每个 spec MUST 标注与 PRD 的追踪关系（对应 FR 与 AC 编号）；实现完成的定义是
  相关 AC 拿到执行证据，而不是代码写完（PRD §25、§28.5）。
- 测试层级（单元 / 合同 / 端到端 / 故障注入）按 PRD §28.3 组织；MUST NOT 通过删除
  难测样本、把风险动作改称"草稿"或降级安全检查来通过验收。
- 每个里程碑以可运行闭环 + 可解释失败退出（PRD §27.2–27.3）；周进度以可演示闭环衡量，
  不以 Agent 类、接口或表的数量衡量。
- 每个较大方案的 `$speckit-plan` MUST 包含先行调研记录：候选开源项目与官方方案、
  license 与维护状态、安全风险、适配成本、交叉对比与选型结论（原则 VIII）。
  不适合复用的部分 MUST 说明自研理由。

## Governance

- 本宪法是仓库内所有 spec、plan、tasks、实现与对外表述的最高约束；任何产出与宪法
  冲突时，宪法优先，冲突必须显式报告而不是静默绕过。
- 修订流程：提出修订理由 → 列出受影响的 FR/AC/里程碑 → 操作者批准 → 更新本文件并
  递增版本（MAJOR：删除或重定义不可妥协原则；MINOR：新增原则或实质性扩展；
  PATCH：文字澄清与勘误）→ 检查既有 spec 是否需要迁移说明。
- 合规审查：`$speckit-plan` 与 `$speckit-implement` 的产出 MUST 对照第 I、II、III 条
  红线自查；发现冲突必须停止并报告。
- 运行时开发指导以 `docs/DevFlow 完整 PRD.md` 为准；其与本宪法冲突处按 Governance
  流程处理。

**Version**: 1.1.0 | **Ratified**: 2026-09-01 | **Last Amended**: 2026-09-01
