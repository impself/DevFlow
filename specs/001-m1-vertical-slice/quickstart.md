# Phase 1 Quickstart: M1 主线竖切验证指南

**Date**: 2026-09-01 | 目标：按 [spec.md](./spec.md) 的 SC-1～SC-5 与相关 AC 做端到端取证。
实现细节（代码结构、任务清单）见 [plan.md](./plan.md) 与 tasks.md，本文件只描述「怎么跑、看到什么算过」。

## 前置条件

1. **数据库**：PostgreSQL 可用（本机 18.1，或 `docker compose -f infra/docker-compose.yml up -d`）。
   创建库 `devflow` 并执行 `services/control/internal/store/migrations`。
2. **GitHub App**（自有账号）：权限仅 Metadata:read、Contents:read、Issues:read/write；
   Webhook 指向本地隧道地址，订阅 `issues`（opened/reopened）；配置 Webhook secret。
   记录 App ID、私钥路径、App 安装到的测试仓库。
3. **测试仓库**：`devflow-fixture`（独立仓库，含 README 与可答复的用法类文档——B01 类素材）。
   ⚠️ 该仓库建仓规格是独立 spec；未就绪前可用任一自有小仓库验证链路。
4. **环境变量**（参考 `infra/.env.example`，真实 `.env` 不入库）：
   `DATABASE_URL`、`GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_PATH`、`GITHUB_WEBHOOK_SECRET`、
   `DASHSCOPE_API_KEY`、`DEVFLOW_INTERNAL_TOKEN`。
5. **本地隧道**：`cloudflared tunnel --url http://localhost:8080`（或 smee 客户端）。

## 启动

```bash
# Go 控制层（API + webhook + 发布器）
go run ./services/control/cmd/api
# Go Runner（领取与执行）
go run ./services/control/cmd/runner
# Python 智能层（先建独立 venv，Python 3.13）
pip install -e services/runtime
uvicorn app.main:app --port 8100
# 前端
cd web && npm i && npm run dev
```

## 验证场景（对应 spec 成功标准）

### 场景 A：有据答复草稿（SC-1，AC03/AC31）

1. 在 `devflow-fixture` 创建用法类 Issue（如「分页参数 page 从 0 还是 1 开始？」）。
2. **预期**：1 秒内 webhook 收到 202；`GET /api/v1/cases` 出现新 Case（`analyzing`）；
   约数十秒后 Case 详情出现答复草稿，每条引用可点开核对到固定 SHA 的文件与行号；
   `GET /api/v1/runs/{id}` 可见提交回执、状态轨迹与 model_calls 费用记录（cost_status）。
3. **判定**：GitHub 上该 Issue **无任何新评论**（AC31）。取证：数据库 inbox_events /
   run_commits / model_calls 各一行 + UI 截图。

### 场景 B：批准后精确发布一条评论（SC-2，AC32/AC33/AC35/AC36）

1. 在任务详情点击批准。
2. **预期**：GitHub Issue 恰好新增一条与草稿摘要一致的评论；actions 表有回执
   （remote_comment_id + remote_url）。
3. 扰动验证：
   - **重复批准**：连点批准 / 带同一 idempotency_key 重放 → 仍只有一条评论，接口回显原结果（AC35）。
   - **响应丢失**：发布时断网 → Action 进入核对路径，恢复网络后核对出远端结果，不重发（AC36）。
   - **内容变化**：批准后（模拟）修改草稿内容 → 原批准失效，要求重新审批（AC33）。
4. **判定**：远端评论数 = 1。取证：approval_bundles / actions 表记录 + GitHub 页面截图。

### 场景 C：信息不足（AC04）

1. 创建「分页不对，帮我修一下」类 Issue。
2. **预期**：Run 结论 NEEDS_INFO，草稿区显示 ≤3 个追问，Case 转等待信息状态，执行槽已释放。

### 场景 D：事件去重与自触发（AC45）

1. 用 gh CLI 重投同一 delivery（`gh api` 重放或临时关闭再恢复 webhook 重放）。
2. **预期**：inbox_events 仍只有一条 `processed`，不重复建 Run；
   DevFlow 自己发的评论触发的事件被识别为 `ignored`。

### 场景 E：崩溃恢复（SC-3，AC23/AC24）

1. 在场景 A 分析进行中（runner 日志显示已领取）强杀 runner 进程（`taskkill /F`）。
2. **预期**：30 秒租约到期后 Run 转 RECOVERING，新 worker 接管（lease_epoch +1）并完成分析；
   已提交内容不丢失，无重复发布。
3. 迟到写回：手动用旧 epoch 调提交路径 → 被拒绝并审计（AC24）。

### 场景 F：接入能力检查（AC01）

1. 用缺 Issues 读权限的 App 安装接入仓库。
2. **预期**：422 返回缺失能力清单，不产生 active 仓库记录。

## 验收对照表

| Spec 项 | 场景 | 证据位置 |
| --- | --- | --- |
| SC-1 / AC03 / AC31 | A | inbox_events、run_commits、model_calls 表 + UI 截图 + GitHub 零写入 |
| SC-2 / AC32/33/35/36 | B | approval_bundles、actions 表 + GitHub 评论数=1 |
| AC04 | C | run.outcome=NEEDS_INFO + 追问内容 |
| AC45 | D | inbox_events 去重记录 |
| SC-3 / AC23/24 | E | runs.lease_epoch 变化轨迹 + runner 日志 |
| AC01 | F | 接入 422 响应 + repositories.status |
| AC47 | A | model_calls.cost_status=estimated（模拟超时） |
| AC50 | 任一 | 停 PG 后 webhook 返回 5xx、UI 显示原因，恢复后自愈 |

## 已知限制（如实记录）

- 本指南在本地开发环境取证；性能数字仅代表本地基线，不得写成通用性能结论（PRD §26.1）。
- devflow-fixture 未就绪时，场景只能验证链路正确性，不构成「真实案例」验收；
  正式 M1 退出仍需 fixture 就绪后的 B01 类真实案例（spec Assumptions）。
