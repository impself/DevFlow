# DevFlow 本地开发环境

## 组成

- **PostgreSQL 18**（Docker 或本机）：业务状态唯一权威（宪法 IV）
- **Go api/runner**：`services/control`
- **Python runtime**：`services/runtime`（AgentScope 2.0）
- **Web**：`web/`（Vite dev 服务器，代理 `/api` 到 :8080）

## 1. 数据库

```bash
cd infra
docker compose up -d          # devflow-db :5432（devflow/devflow/devflow）
# 或使用本机 PG：改 .env 的 DATABASE_URL
```

迁移随 api/runner 启动自动执行（`internal/store/migrations`，幂等）。

## 2. 环境变量

```bash
cp infra/.env.example infra/.env   # 填好后 export（或用 dotenv 工具）
```

| 变量 | 说明 | 谁消费 |
| --- | --- | --- |
| `DATABASE_URL` | PG 连接串 | api + runner |
| `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY_PATH` / `GITHUB_WEBHOOK_SECRET` | GitHub App 三件套 | api + runner |
| `DASHSCOPE_API_KEY` | qwen-plus 模型凭据 | runtime |
| `DEVFLOW_INTERNAL_TOKEN` | Go↔Python 内部令牌 | 双方 |
| `OPERATOR_TOKEN` | 工作台操作者令牌 | api + web |
| `RUNTIME_URL` / `ARTIFACT_DIR` | 智能层地址 / 产物目录 | runner |

## 3. GitHub App 注册（最小权限）

1. github.com → Settings → Developer settings → New GitHub App；
2. 权限（最小集，宪法 III）：**Metadata: read / Contents: read / Issues: read+write**；
   不勾任何 write-contents/admin；
3. Webhook URL 指向隧道地址（见下），secret 即 `GITHUB_WEBHOOK_SECRET`；
   订阅事件：**只勾 Issues**；
4. 生成私钥下载 → 路径填 `GITHUB_APP_PRIVATE_KEY_PATH`；
5. 安装到测试仓库，记下 installation ID（接入 API 用）。

## 4. Webhook 隧道（本地接收 GitHub 事件）

```bash
# 方案 A：cloudflared（推荐，无需账号）
cloudflared tunnel --url http://127.0.0.1:8080
# 方案 B：smee.io
npx smee-client -u https://smee.io/<你的频道> -t http://127.0.0.1:8080
```

把隧道给出的 HTTPS 地址填进 App 的 Webhook URL（`https://…/webhook`）。

## 5. 启动顺序

```bash
# 1. 智能层
cd services/runtime && .venv/Scripts/python -m uvicorn app.main:app --port 8100
# 2. api（:8080）
cd services/control && go run ./cmd/api
# 3. runner（另开终端）
go run ./cmd/runner
# 4. web（:5173，可选）
cd web && npm run dev
```

健康检查：`curl :8080/healthz`（database:true）、`curl :8100/healthz`
（dashscope_configured:true）。

## 6. 端到端验收

按 `specs/001-m1-vertical-slice/quickstart.md` 场景 A–F 执行；
恢复场景 E 的取证步骤见 `tests/control/recovery_manual_test.md`。
