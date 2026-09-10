# Tasks: M1 主线竖切——Issue 接收到有据答复与批准发布

**Input**: Design documents from `/specs/001-m1-vertical-slice/`

**Prerequisites**: plan.md、spec.md、research.md、data-model.md、contracts/（openapi.yaml + 4 个 JSON Schema）、quickstart.md

**Tests**: 已包含合同测试与租约协议集成测试（plan.md 测试策略明确要求，宪法 IV/V 落实）。

## 执行约定：教学式逐任务 review（操作者要求，跨会话有效）

1. **每完成一个任务**：实现 → 自测通过 → 向操作者讲解（本次改动内容、关键语法、
   背后原理、为什么这样写、放弃了哪些备选方案）→ **暂停等待操作者 review**。
2. 操作者明确确认后：勾选本清单对应项 → `git commit`（一个任务一个提交）→ `git push` →
   才开始下一任务。**任务未经确认前不 commit、不 push**。
3. `[P]` 并行任务可合并实现与讲解，但需逐文件说明。
4. 讲解目标是提升代码架构能力：重点讲设计决策与权衡（分层、接口边界、错误语义、
   为什么 A 不是 B），不逐行翻译代码。
5. Checkpoint 处按 quickstart.md 场景做端到端验证后再进入下一 Phase。

## Format: `[ID] [P?] [Story] Description`

- **[P]**: 可并行（不同文件、无未完成依赖）
- **[Story]**: 所属用户故事（US1/US2/US3，对应 spec.md）
- 路径均相对仓库根

---

## Phase 1: Setup（项目初始化）

- [x] T001 创建目录骨架与工程文件：`services/control/{cmd/{api,runner},internal/{store,github,controller,policy,publisher}}`、`services/runtime/app/{agents,contracts}`、`web/`、`infra/`、`tests/`；初始化 `go.mod`（module devflow/control）、`infra/docker-compose.yml`（可选 PG 18）、`infra/.env.example`
- [x] T002 [P] Go API 服务骨架：`services/control/cmd/api/main.go`（Gin + `/healthz` 含依赖状态）、`internal/config/config.go`（env 配置加载与启动校验，缺失配置明确报错——PRD §29.3）
- [x] T003 [P] Python 智能层骨架：`services/runtime/pyproject.toml`（agentscope==2.0.7、fastapi、pydantic、uvicorn，Python 3.13 venv 说明）、`app/main.py`（FastAPI + `/healthz`）
- [x] T004 [P] 前端脚手架：Vite + React + TypeScript（`web/`），最小依赖（不引组件库），代理到 `:8080`
- [x] T005 [P] 横切设施：结构化日志（`log/slog`）封装、`X-DevFlow-Internal-Token` 内部认证 Gin 中间件、OTel 初始化占位（`internal/obs/`）

## Phase 2: Foundational（阻塞所有用户故事）

**⚠️ 本阶段完成前不得开始任何用户故事**

- [x] T006 数据库迁移 V1：`services/control/internal/store/migrations/0001_init.sql` 按 data-model.md 建 10 张表（operators、repositories、inbox_events、cases、runs、run_commits、artifacts、reply_drafts、approval_bundles、actions、model_calls），含 delivery_id 唯一约束、runs 部分索引（QUEUED/RECOVERING）、(run_id, commit_id) 主键
- [x] T007 [P] sqlc 查询集：`internal/store/queries/*.sql`（inbox 去重插入、claim_run 的 `FOR UPDATE SKIP LOCKED` 领取、心跳续租 `WHERE lease_epoch=$n`、commit 幂等 `ON CONFLICT` 回执、case/run 查询）+ `sqlc.yaml` + 生成代码入库
- [x] T008 Store 封装：`internal/store/store.go`（pgx 连接池、`WithTx` 事务辅助、错误语义映射），被 controller/publisher 依赖
- [x] T009 [P] GitHub 接入层：`internal/github/client.go`（ghinstallation App JWT + installation token 的 RoundTripper、go-github client 工厂、按仓库取 client）、`internal/github/webhook.go`（raw body + `ValidateSignature` + `ParseWebHook` 封装）
- [x] T010 Runner 骨架：`services/control/cmd/runner/main.go` + `internal/controller/worker.go`（worker goroutine 池、并发上限 1、context 优雅停机、心跳循环挂钩子）

**Checkpoint**: `go run ./cmd/api` 与 `./cmd/runner` 可启动，`/healthz` 绿，迁移可重复执行

---

## Phase 3: User Story 1——Issue 自动生成有依据的答复草稿（P1）🎯 MVP

**Goal**: 测试仓库创建 Issue → 系统自动分析 → 带引用的答复草稿可查；GitHub 零写入

**Independent Test**: quickstart.md 场景 A（答复草稿 + 引用可核对）、C（NEEDS_INFO）、D（去重）

### 实现任务

- [ ] T011 [US1] Webhook 入口：`internal/controller/webhook.go` + 路由注册——`io.ReadAll` 取 raw body → `ValidateSignature` → inbox_events 落库（delivery_id 冲突即去重返回 200）→ **同一事务**创建/更新 case 并插入 run（QUEUED）→ 返回 202；目标 1 秒内响应
- [ ] T012 [US1] 事件过滤与业务去重：`internal/controller/events.go`——只处理 issues opened/reopened；范围外仓库记 ignored；自评论（bot 自己的 comment）去重防循环（AC45）
- [ ] T013 [US1] 租约协议：`internal/controller/lease.go`——claim_run（SKIP LOCKED 领取 + epoch+1 + 30s 租约）、heartbeat（0 行受影响即失权）、提交时校验 lease_epoch（旧 epoch 拒绝，AC24）、取消检查
- [ ] T014 [US1] Run 执行流：`internal/controller/execute.go`——领取后预取固定 head SHA 的 README/相关文件（go-github Contents API，≤N 文件大小上限）→ 组装 issue-analysis 请求调 Python → 成功后构造 `commit_id + payload_hash` 原子提交（`ON CONFLICT` 幂等回执，AC25/26）→ 更新 Run 终态与 outcome
- [ ] T015 [P] [US1] Python 合同镜像：`services/runtime/app/contracts/models.py`（Pydantic 模型严格镜像 `contracts/schemas/issue-agent-output.schema.json`，含条件校验：ANSWER_READY 必须有 reply+evidence、degraded 不得 ANSWER_READY）+ `tests/contract/test_issue_agent_output.py`（用 jsonschema 库对示例与 schema 双向校验）
- [ ] T016 [US1] Issue Agent：`services/runtime/app/agents/issue_agent.py`——AgentScope 2.0 `Agent` + `DashScopeChatModel(model="qwen-plus")` + `structured_schema` 结构化输出；`structured_output is None`（EXCEED_MAX_ITERS）与校验失败处理：重试 1–2 次 → 降级 NEEDS_INFO（degraded=true）；usage（tokens + cost estimated）回传
- [ ] T017 [US1] 分析端点：`services/runtime/app/api.py`——`POST /internal/v1/issue-analysis`（鉴权中间件、超时控制、异常 → 503 让 Runner 走重试）
- [ ] T018 [US1] 产物落库：`internal/controller/artifacts.go`——草稿正文写 content-addressable 文件（sha256 命名）+ artifacts 表引用；reply_drafts（evidence jsonb、body_digest、status=current，旧草稿标 superseded）
- [ ] T019 [P] [US1] 租约与幂等集成测试：`tests/control/lease_test.go`（go test + 真实 PG：并发领取互斥、心跳续租、过期租约被接管、旧 epoch 提交被拒、同 commit_id 幂等回执、同 ID 不同 payload_hash 冲突——AC23/24/25/26）

**Checkpoint**: quickstart 场景 A/C/D 通过——真实 Issue 生成带引用草稿、GitHub 零写入、NEEDS_INFO ≤3 问、重复投递不重复建 Run

---

## Phase 4: User Story 2——批准后精确发布一条评论（P1）

**Goal**: 操作者审阅草稿并批准 → 目标 Issue 恰好新增一条与批准内容一致的评论；重复/重试/断网都不产生第二条

**Independent Test**: quickstart.md 场景 B 含三类扰动（重复批准、响应丢失、内容变化）

- [ ] T020 [US2] 审批包生成：`internal/controller/approval.go`——draft → approval_bundles（target 精确到 repo_numeric_id+issue_number、content_digest=draft body_digest、expires_at=+24h、idempotency_key 唯一）
- [ ] T021 [US2] 批准/拒绝端点：`cmd/api` 路由 + `internal/controller/approval.go`——操作者服务端认证（M1 单操作者 token，不信任请求声明身份，AC32）、过期或内容/目标变化返回 409（AC33）、重复批准按 idempotency 回显原结果（AC35）
- [ ] T022 [US2] 发布前重核：`internal/publisher/preflight.go`——执行前重查 Issue 未关闭、无新的相关人类回复、草稿仍适用；不满足则 bundle 标 EXPIRED 并说明（FR-8）
- [ ] T023 [US2] 评论发布与回执：`internal/publisher/comment.go`——go-github `Issues.CreateComment`，action 状态机 PENDING→EXECUTING→SUCCEEDED/FAILED，落 action-receipt（remote_comment_id + remote_url）
- [ ] T024 [US2] 响应丢失核对：`internal/publisher/reconcile.go`——超时/网络错误进入 RECONCILING：按目标+身份拉取 Issue 评论列表按 content_digest 核对，命中即 SUCCEEDED 并记录 remote_comment_id；不命中且无不确定证据才允许人工决策，绝不自动重发（AC36）
- [ ] T025 [US2] 审批合同测试：`tests/contract/test_approval_bundle.py`（approval-bundle / action-receipt schema 校验 + 过期/失效规则用例）

**Checkpoint**: quickstart 场景 B 通过——评论数恒等于 1，三类扰动全部验证

---

## Phase 5: User Story 3——任务与恢复的最小可见性（P2）

**Goal**: 工作台可见 Case/Run 状态、草稿、证据、费用与原始事件；崩溃后可恢复可取证

**Independent Test**: quickstart.md 场景 E（杀进程 → RECOVERING → 接管）、F（能力缺失 422）+ 界面走查

- [ ] T026 [US3] 对外查询与接入 API：`cmd/api` 路由 + `internal/controller/query.go`——GET repositories/cases/cases/{id}/runs/{id}（含提交轨迹与 model_calls）、POST repositories（能力检查，缺失 422 返回清单，AC01）、POST runs/{id}/cancel（CANCEL_REQUESTED 最小状态机）
- [ ] T027 [US3] 前端列表与接入页：`web/src/pages/RepoSetup.tsx`、`web/src/pages/CaseList.tsx`——接入表单（422 展示缺失能力）、Case 列表（状态徽标、来源 Issue 链接）
- [ ] T028 [US3] 前端任务详情页：`web/src/pages/CaseDetail.tsx`——输入快照、草稿 Markdown（sanitize 渲染，PRD §6.4）、证据列表（path+lines+sha，可点开引用原文）、Run 状态轨迹、model_calls 费用、批准/拒绝按钮（批准前显示目标与内容摘要）
- [ ] T029 [US3] 恢复取证辅助：`tests/control/recovery_manual_test.md`——场景 E 操作步骤（强杀 runner → 观察租约到期接管与 lease_epoch 变化 → 旧 epoch 写回被拒的日志/审计位置）

**Checkpoint**: 场景 E/F 通过；三个页面走查无红灯状态（PRD §6.4 必须设计的状态子集）

---

## Phase 6: Polish & Cross-Cutting

- [ ] T030 [P] 部署与启动文档：完善 `infra/.env.example` 与 `infra/README.md`（本地 PG 或 Docker、cloudflared/smee 隧道、GitHub App 注册步骤与最小权限清单）
- [ ] T031 [P] 文档路径修复：`docs/AliGo 架构学习与 DevFlow 映射.md` 内指向旧 `调研/` 目录的链接改为仓库内 `docs/` 相对路径
- [ ] T032 quickstart 全量回归：按 `specs/001-m1-vertical-slice/quickstart.md` 场景 A~F 逐项取证，填写验证记录（含截图/SQL 证据位置），AC 证据对照表闭环
- [ ] T033 宪法合规自查：对照宪法 I~VIII 红线过一遍代码与配置（无合并入口、审批绑定、凭据隔离、schema 校验、预算参数与 PRD §21.5 一致），结果记入验证记录

---

## Dependencies & Execution Order

### Phase 依赖

- Setup（T001–T005）→ Foundational（T006–T010）→ **US1（T011–T019）→ US2（T020–T025）→ US3（T026–T029）** → Polish（T030–T033）
- US2 依赖 US1 的草稿产物；US3 的详情页依赖 US1/US2 的数据。单人开发按编号顺序执行即可，`[P]` 任务可在当前步骤内穿插。

### 关键路径

T001 → T002 → T006 → T007 → T008 → T010 → T011 → T013 → T014 → T016 → T017 → T018 → T020 → T021 → T023 → T026 → T028 → T032

### Parallel Opportunities

- T002/T003/T004/T005 互不依赖可并行
- T015（Python 合同）与 Go 侧 T013/T014 无依赖，可先行
- T019（集成测试）只依赖 T006/T007/T008 + T013，可在 T014 前写（测试先行）
- T030/T031 随时可做

---

## Implementation Strategy

### MVP First（推荐节奏）

1. Phase 1 + Phase 2 完成 → 基础设施就绪
2. Phase 3（US1）→ **STOP**：quickstart 场景 A/C/D 端到端验证 → 这已经是可演示的 MVP
3. Phase 4（US2）→ 场景 B → 主线闭环完整（可对外演示的核心）
4. Phase 5（US3）→ 场景 E/F + 界面
5. Phase 6 → 回归与合规

### 教学式推进（操作者要求）

每个任务 = 实现 + 自测 + 讲解 + review + 独立 commit；Checkpoint 处做端到端验证。
预计 US1 是工作量最大的阶段（跨语言合同 + 租约协议），讲解密度也最高。

## Notes

- AgentScope 必须用 2.0 API（docs.agentscope.io），1.x 教程写法（structured_model/api_key）已失效——见 research.md §5
- 所有对外数据结构以 `contracts/schemas/` 为唯一真源；实现语言侧的模型（Pydantic/Go struct）是镜像，schema 改动必须先改合同
- 模型费用逐次记录 model_calls；cost_status 只能是 estimated/confirmed，不得记零（AC47）
- 本清单不含 devflow-fixture 建仓（独立 spec 002）——M1 正式退出验收前需就绪
