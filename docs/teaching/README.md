# Spec-001 教学笔记索引

M1 主线竖切逐任务教学笔记 + [架构×业界对照表](./research-alignment.md)（面试弹药库）：每个任务完成后记录
改动内容、关键语法、背后原理、设计权衡与放弃的备选方案，供操作者 review。
> **教学重点（2026-09-11 更新，面试导向）**：笔记聚焦 agent 工程、后端架构与 Go 语法；
> 前端/部署等外围任务只记结论，不展开。


> 执行模式（2026-09-10 操作者更新）：任务完成后**不再暂停等待确认**，自动进入下一任务；
> 本目录即事后 review 的载体。

| 任务 | 笔记 | 主题 |
| --- | --- | --- |
| T002 | [T002-go-api-skeleton.md](./T002-go-api-skeleton.md) | Go API 骨架：优雅停机、fail-fast 配置、healthz 依赖状态 |
| T003 | [T003-python-runtime-skeleton.md](./T003-python-runtime-skeleton.md) | Python 智能层：pyproject、venv、degraded 语义 |
| T004 | [T004-frontend-scaffold.md](./T004-frontend-scaffold.md) | 前端脚手架：Vite 代理、TS 严格模式、手工搭建理由 |
| T005 | [T005-cross-cutting.md](./T005-cross-cutting.md) | 横切设施：slog 收口、constant-time 鉴权、追踪占位 |
| T006 | [T006-db-migrations.md](./T006-db-migrations.md) | 数据库 V1：11 张表、部分索引、幂等迁移器、测试清理纪律 |
| T007 | [T007-sqlc-queries.md](./T007-sqlc-queries.md) | sqlc 查询集：SKIP LOCKED 领取、epoch 心跳、幂等回执 |
| T008 | [T008-store-facade.md](./T008-store-facade.md) | Store 门面：WithTx 事务作用域、错误语义映射、夹具生命周期 |
| T009 | [T009-github-access.md](./T009-github-access.md) | GitHub 接入层：App/installation 身份、RoundTripper、HMAC 验签、review 工作流首秀 |
| T010 | [T010-runner-worker.md](./T010-runner-worker.md) | Runner 引擎：领取循环、心跳失权、fencing、防毒循环、每包专属测试库 |
| T011 | [T011-webhook-entry.md](./T011-webhook-entry.md) | Webhook：HTTP 语义×事务×幂等去重、闭包遮蔽陷阱（面试考点向） |
| T012 | [T012-event-filtering.md](./T012-event-filtering.md) | 触发面防线：过滤规则、防循环、纯函数+表驱动（agent 工程考点） |
| T013 | [T013-lease-protocol.md](./T013-lease-protocol.md) | 提交回执协议：幂等键+内容哈希、fencing 回滚、合作式取消（面试考点） |
| T014 | [T014-execute-flow.md](./T014-execute-flow.md) | 执行流：合同复验信任边界、确定性幂等键、固定快照+SHA、预算三层护栏 |
| T015 | [T015-python-contract-mirror.md](./T015-python-contract-mirror.md) | 跨语言合同：Pydantic 镜像、双向校验抓 null/缺省漂移（agent 工程考点） |
| T016 | [T016-issue-agent.md](./T016-issue-agent.md) | Issue Agent：结构化输出四层防线、重试预算、显式降级、行号注入（核心 agent 考点） |
| T017 | [T017-analysis-endpoint.md](./T017-analysis-endpoint.md) | 分析端点：503 重试语义、constant-time 鉴权、FastAPI 序列化陷阱 |
| T018 | [T018-artifacts.md](./T018-artifacts.md) | 产物落库：content-addressable、先文件后库、supersede 事务不变量 |
| T019 | [T019-lease-concurrency-tests.md](./T019-lease-concurrency-tests.md) | 并发协议测试：16 抢 8 互斥、续租时序断言、接管全链路导演 |
| T020 | [T020-approval-bundle.md](./T020-approval-bundle.md) | 审批包：目标×内容×时限三元组、结构派生幂等键、JSON 大整数精度坑 |
| T021 | [T021-approval-endpoints.md](./T021-approval-endpoints.md) | 批准/拒绝端点：薄 handler、三 AC 会师、中间件泛化时机 |
| T022-024 | [T022-024-publisher-chain.md](./T022-024-publisher-chain.md) | 发布链：错误分类学、恢复语义、reconciliation（面试重点） |
| 硬化 | [hardening-agent-safety.md](./hardening-agent-safety.md) | 调研驱动硬化：sender.type 防循环、引用逐字门、毒 run 预算（面试高价值） |
| T025 | tests/contract/test_approval_bundle.py | 审批合同测试：12 用例，抓到 Go 侧缺 case_id/draft_id 的形状缺口 |
| T026 | [T026-query-api.md](./T026-query-api.md) | 查询/接入 API：读模型组装、能力实测 422、单 SQL 取消状态机 |
