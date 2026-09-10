# T003 教学笔记：Python 智能层骨架

> 对应文件：`services/runtime/pyproject.toml`、`app/main.py`、各包 `__init__.py`
> 一句话：给"会思考的大脑"（AgentScope + qwen）搭好宿舍——FastAPI 进程能起、
> `/healthz` 能如实汇报"我现在有没有资格干活"。

## 1. 为什么骨架这么小，却值得单独一个任务

runtime 是全系统唯一的"智能"组件，也是唯一非 Go 组件。它的骨架要先立起三条规矩：

1. **依赖版本有法可依**（pyproject.toml 是唯一真源）；
2. **无状态、无数据库**——宪法 III：Python 永远不碰库凭据，只收固定输入、吐结构化结果；
3. **健康语义诚实**：没配模型 key 就报 `degraded`，不装能干。

## 2. pyproject.toml：Python 工程的"户口本"

### 2.1 为什么不用 requirements.txt？

| | requirements.txt | pyproject.toml (PEP 621) |
| --- | --- | --- |
| 定位 | pip 的安装清单 | 项目元数据 + 依赖 + 工具配置**三合一** |
| 谁消费 | 只有 pip | pip、pytest、mypy、ruff、IDE 全都认 |
| `pip install -e .` | 不适用 | 一条命令完成"安装本项目 + 依赖" |

`requirements.txt` 是"购物清单"，`pyproject.toml` 是"身份证"。现代 Python 项目（含
AgentScope 自己）都已迁到后者。M1 只有一个运行环境，不引入 uv/poetry 等额外工具链
（宪法 VIII：能少一个工具就少一个）。

### 2.2 版本钉法：为什么 agentscope 钉死、fastapi 只给下限？

```toml
"agentscope==2.0.7",        # 精确钉死
"fastapi>=0.115",           # 只设下限
```

- **agentscope==2.0.7**：research.md §5 的核心结论——2.0 是推倒重写，1.x 教程
  （`structured_model=`、`api_key=` 参数）全部失效。这种"API 地震带"上的库必须钉死，
  升级=重新验证一遍所有用法，不能让 `pip install` 静默升上去。
- **fastapi/pydantic/uvicorn**：我们用的都是稳定十年量级的基础 API（路由、声明模型），
  下限保证安全特性即可，让 pip 自由解析到最新兼容版。
  实测解析结果：fastapi 0.141.1、pydantic 2.13.5——与 plan.md 预期一致。

> 语法点：`>=` 与 `==` 混用是"分层钉版本"策略——**越靠近地震带钉得越死**。

### 2.3 `[project.optional-dependencies]`：测试依赖住"客房"

```toml
[project.optional-dependencies]
dev = ["pytest>=8", "jsonschema>=4.23"]
```

pytest 只在开发/CI 需要，生产容器不需要——装进 `dev` extra，`pip install -e ".[dev]"`
时才带上。方括号语法就是"附带买下可选配件"。

## 3. venv：为什么不直接装进 conda base？

```bash
python -m venv .venv          # 项目内虚拟环境
.venv\Scripts\pip install -e ".[dev]"
```

- **隔离**：conda base 是公共客厅，项目依赖（尤其钉死版本的 agentscope）不能污染它；
- **可丢弃**：`.venv/` 在 .gitignore 里，坏了删掉重建，30 秒恢复；
- **可复现**：别人拿到仓库后照 README 两条命令得到一致环境。

`.venv` 这个目录名是工具生态的默认约定（VS Code、pytest 都自动识别）。

## 4. main.py 的三个语法/设计点

### 4.1 `from __future__ import annotations`

```python
from __future__ import annotations
```

让类型注解全部按"字符串"延迟求值（PEP 563）。好处：注解里可以引用还没定义的类、
避免循环导入；Python 3.13 原生速度更快。属于"写了不亏"的现代约定。

### 4.2 `def healthz() -> dict:`——FastAPI 的返回值即响应

```python
@app.get("/healthz")
def healthz() -> dict:
    return {"status": ..., "dashscope_configured": ...}
```

FastAPI 把返回的 dict 自动序列化成 JSON 响应——不需要手动 `jsonify`。
对比 Go 版（T002）的 `c.JSON(code, gin.H{...})`：Go 是显式写响应，Python 是**声明式**
——路由注册和返回值类型就是文档，`/docs` 页面自动生成。

### 4.3 `bool(os.environ.get("DASHSCOPE_API_KEY"))`——只报状态，不回显内容

健康检查验证"配置了没有"，绝不能把密钥值带出去（哪怕内网）。`bool(...)` 把任意值
收敛成布尔——存在 True，不存在/空串 False。这是凭据处理的通用纪律：**探针只要
 yes/no，不要 value**。

## 5. degraded 但返回 200：和 Go 版 healthz 的语义分歧（有意为之）

| | Go API 的 /healthz | runtime 的 /healthz |
| --- | --- | --- |
| 数据库挂 | 503 | —（无数据库） |
| 模型 key 未配 | — | **200 + degraded** |

为什么 runtime 不返回 503？健康检查有两类消费者：
- **进程监控**（要重启吗）：runtime 进程本身完全健康，重启 100 遍也变不出 key → 该 200；
- **上游调度**（能把分析请求发过来吗）：看 body 里的 `dashscope_configured` 字段决定。

而 Go API 的数据库是**每个请求都碰**的硬依赖，挂了就必须 503 让基础设施把它摘出负载
均衡。依赖的"硬度"不同，语义就不同——健康检查不是抄模板，是表态。

## 6. 自测证据

| 场景 | 结果 |
| --- | --- |
| 版本钉死验证 | `agentscope 2.0.7, fastapi 0.141.1, pydantic 2.13.5` ✅ |
| 无 key 启动 `/healthz` | `{"status":"degraded","dashscope_configured":false}` HTTP 200 ✅ |
| 有 key 启动 `/healthz` | `{"status":"ok","dashscope_configured":true}` ✅ |

## 7. 下一站预告

T015 的 Pydantic 合同镜像住进 `app/contracts/`，T016 的 AgentScope Agent 住进
`app/agents/`，T017 的 `/internal/v1/issue-analysis` 端点挂进本文件的 app。
宿舍已建好，住户即将入住。
