# Spec-001 教学笔记索引

M1 主线竖切（specs/001-m1-vertical-slice）逐任务实现的教学讲解：每个任务完成后记录
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
