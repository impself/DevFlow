# Implementation Plan: M1 主线竖切——Issue 接收到有据答复与批准发布

**Branch**: `001-m1-vertical-slice`（单人开发，直接提交 main，目录号即本特性标识） | **Date**: 2026-09-01 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-m1-vertical-slice/spec.md`

## Summary

实现 DevFlow 第一条端到端主线：GitHub App + Webhook 接收自有测试仓库的 Issue 事件，
Go 控制层验签、去重、落库并创建 Case/Run（数据库租约协议）；Python 智能层用
AgentScope 对固定输入快照生成带引用的答复草稿（结构化输出 + 外层校验）；最小 Web
工作台展示任务与证据，操作者批准后由 Go 发布器精确发布一条 Issue 评论（幂等 +
结果核对）。技术选型依据见 [research.md](./research.md)（原则 VIII 先行调研产物）。

## Technical Context

**Language/Version**:
- 控制层/Runner/发布器：Go 1.24（本机 `C:\Program Files\Go\bin`）+ Gin
- 智能层：Python 3.13（`D:\conda\python.exe` 建独立 venv）+ FastAPI 0.141 + Pydantic 2.13 + agentscope 2.0.7（钉住 minor，2.0 文档站 docs.agentscope.io）
- 前端：Node 22 + React + TypeScript + Vite（最小工作台，不引组件库）

**Primary Dependencies**:
- Go：`jackc/pgx/v5`（MIT）+ `sqlc` 生成数据访问（MIT）；`google/go-github/v90`（BSD-3，含 webhook 验签 `ValidatePayload`/`ValidateSignature`）；`bradleyfalzon/ghinstallation/v2`（Apache-2.0，GitHub App token）；Gin（MIT）
- Python：agentscope 2.0.7（Apache-2.0，`DashScopeChatModel` + `DashScopeCredential`，模型 `qwen-plus` 起步）；FastAPI + uvicorn
- 不引入：Redis/Kafka/Temporal、River（MPL-2.0 + Pro 功能墙）、gue（长事务模型）、asynq（Redis）、ORM、向量检索

**Storage**: PostgreSQL（本机 18.1 已装；`infra/docker-compose.yml` 提供可选容器化）
业务状态权威来源；大内容（草稿正文、引用原文）存本地 Artifact 目录（content-addressable，sha256 命名），库存引用与哈希（PRD §18.1）

**Testing**: `go test`（单元 + 租约协议集成测试）、`pytest`（智能层合同测试：结构化输出 schema 校验、降级路径）、合同测试以 `contracts/` 下 JSON Schema 为准；端到端按 `quickstart.md` 手动验收（M1 不建 E2E 框架）

**Target Platform**: 本地 Windows 开发；webhook 经隧道（cloudflared / smee.io）接入 GitHub；生产形态（Linux + Docker）M5 再固化

**Project Type**: 多服务单仓（Go control 服务 + Python runtime 服务 + React web），目录结构沿用 PRD §28.1 收窄到 M1

**Performance Goals**: webhook 1 秒内完成持久化并响应（PRD §26.2）；列表/状态 API 本地基线 P95 < 500 ms；租约到期后 60 秒内开始恢复（PRD §26.2）

**Constraints**: 同时最多 2 Run（M1 实际并发 1）；单 Run ≤900 秒、≤24 次模型调用；审批包 24 小时有效期；对外动作仅 `POST_ISSUE_COMMENT`；模型 API 月预算 300–500 元（qwen-plus 起步）

**Scale/Scope**: 单操作者 + 1 个 `devflow-fixture` 测试仓库；M1 数据模型 10 张表（见 data-model.md）

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| 原则 | 检查项 | 结论 |
| --- | --- | --- |
| I 自动化红线 | 本切片对外动作只有 `POST_ISSUE_COMMENT`；无合并入口、无分支写入、无强推 | ✅ 通过 |
| II 人工审批 | ApprovalBundle 绑定精确目标+内容摘要，24h 有效；批准后内容/目标变更即失效；服务端认证操作者身份；重复批准幂等 | ✅ 通过（data-model 状态机落实） |
| III 最小权限与隔离 | M1 无代码执行（Runner/容器属 M2）；GitHub App 只申请 Metadata/Contents/Issues 读 + Issues 写；凭据只在 Go 侧，Python 通过内部接口取固定 SHA 文件内容 | ✅ 通过 |
| IV 合同优先 | `contracts/` 先行定义 IssueAgentOutput / RunCommit / ApprovalBundle / ActionReceipt 的 JSON Schema，跨语言经 schema 校验，Python 不持数据库写账号 | ✅ 通过（Phase 1 产物） |
| V 证据驱动 | quickstart.md 逐条映射 spec 的 SC 与 AC 取证方式；research.md 中未核实事实显式标注 | ✅ 通过 |
| VI 持久幂等 | runs 表租约协议（SKIP LOCKED 领取、5s/30s、lease_epoch）、commit_id+payload_hash 幂等回执、delivery_id 去重 | ✅ 通过 |
| VII 范围纪律 | 仅 M1 范围（无补丁/隔离/DAG/分支发布）；预算参数进配置并记录 model_calls | ✅ 通过 |
| VIII 复用优先 | Phase 0 已完成三路调研（同类项目/Go 基础设施/AgentScope），license 全部核实，自研部分给出理由 | ✅ 通过（research.md） |

**Post-design re-check**: ✅ 全部通过，无需 Complexity Tracking 豁免。

## Project Structure

### Documentation (this feature)

```text
specs/001-m1-vertical-slice/
├── plan.md              # 本文件
├── research.md          # Phase 0：三路调研与选型结论
├── data-model.md        # Phase 1：M1 实体、表、状态机
├── quickstart.md        # Phase 1：端到端验证指南
├── contracts/           # Phase 1：OpenAPI + JSON Schema
│   ├── openapi.yaml
│   └── schemas/
│       ├── issue-agent-output.schema.json
│       ├── run-commit.schema.json
│       ├── approval-bundle.schema.json
│       └── action-receipt.schema.json
├── checklists/
│   └── requirements.md  # spec 质量清单（$speckit-specify 产物）
└── tasks.md             # Phase 2（$speckit-tasks 生成，尚不存在）
```

### Source Code (repository root)

```text
services/
  control/                  # Go 服务（M1：API 与 Runner 两个进程入口）
    cmd/api/                # webhook 入口 + 对外 API + 审批 + 发布器
    cmd/runner/             # Run 领取执行器（心跳、调用 runtime、提交结果）
    internal/store/         # sqlc 查询 + pgx 连接 + 迁移
    internal/github/        # ghinstallation token、webhook 验签与解析
    internal/controller/    # Case/Run 状态机、租约协议、幂等回执
    internal/policy/        # 仓库能力检查、发布策略（M1 仅评论）
    internal/publisher/     # 执行已批准 Action 与远端结果核对
services/runtime/           # Python 智能层
    app/main.py             # FastAPI 入口（internal 协议）
    app/agents/issue_agent.py   # AgentScope Agent + 结构化输出 + 降级
    app/contracts/          # Pydantic 模型（镜像 contracts/schemas）
web/                        # React+TS+Vite：仓库接入、Case 列表、任务详情+审批
contracts/                  # 同 specs 目录 contracts（实现时以此为唯一真源复制引用）
infra/                      # docker-compose.yml（可选 PG）、.env.example、隧道说明
tests/                      # 合同测试（schema 校验）、租约协议集成测试
docs/                       # 现有 PRD 文档
```

**Structure Decision**: 采用 PRD §28.1 建议的 `services/control` + `services/runtime` +
`web` + `contracts` + `infra` + `tests` 布局，M1 阶段不创建 `evals/`（M5），
`internal/runner` 与 `internal/publisher` 先并入最小实现（M2 拆分 Runner 独立进程时再解耦）。
`devflow-fixture` 为独立仓库，不在本仓内。

## Complexity Tracking

> 无宪法违例，本表留空。

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
