# T017 教学笔记：分析端点——为「调用方的重试路径」设计响应语义

> 对应文件：`app/api.py`、`app/main.py` 接线、`tests/test_api.py`
> 面试视角：服务间 API 的**状态码语义设计** + Python 侧 constant-time 鉴权 +
> 一个值得记住的序列化陷阱（FastAPI response_model 绕过 model_dump_json）。

## 1. 状态码是给"重试决策器"看的

端点只有四种出口，每种都对应 Go Runner 的一个动作：

| 状态码 | 场景 | Go Runner 的动作 |
| --- | --- | --- |
| 200 | 分析成功 | 原子提交落库 |
| 401 | 内部令牌缺失/错误 | **不重试**（配置错误，人工修） |
| 422 | 请求体违反合同 | 不重试（请求构造有 bug） |
| 503 | 超时/模型过载/内部异常 | **重试**（临时态） |

关键决策：**内部异常统一 503 而不是 500**。503 的语义是"服务暂不可用，稍后
重试会好"；500 是"请求有错，重试无意义"。分析失败几乎总是临时态（模型超时、
供应商抖动），把可重试的错误标成不可重试，等于放弃了系统的自愈能力。

超时也归 503：Python 侧 240s 上限**故意小于** Go 侧 300s 兜底——让超时先在
有上下文的一侧发生，给出带语义的错误而不是让对端超时瞎猜。

## 2. Python 的 constant-time 比较：`secrets.compare_digest`

与 Go 侧 `subtle.ConstantTimeCompare`（T005）、`hmac.Equal`（T009）完全同源的
纪律：凡比较密钥，不用 `==`（短路比较泄漏计时信息），用 constant-time 原语。
`secrets` 模块是 Python 标准库——跨三种语言同一个考点：**计时侧信道防御不挑语言**。

密钥未配置直接 401（fail-fast）：空密钥等于没锁门，任何攻击者都能猜中空串。

## 3. 实战陷阱：FastAPI 绕过 model_dump_json

T015 在 Pydantic 模型上覆写了 `model_dump_json`（None 字段缺省而非 null），
端点测试立刻抓到：FastAPI 的 `response_model` 序列化走的是**自己的路径**
（`model_dump(mode="json")`），覆写 `model_dump_json` 管不到它。

修法：改用 Pydantic v2 的 `model_serializer` 钩子——对 `model_dump`、
`model_dump_json`、FastAPI 响应三条路径统一生效：

```python
@model_serializer
def _serialize_without_none(self) -> dict[str, Any]:
    return {k: v for v in self.__dict__.values() if v is not None}
```

教训：**框架的序列化入口可能不止一个，「在某一层覆写」不如「在模型层声明」**。

## 4. 异常处理的边界纪律

```python
except TimeoutError:  → 503 "分析超时"
except RuntimeError:  → 503 "智能层未就绪"（典型：API key 未配）
except Exception:     → 503，详情只进日志（logger.exception 带堆栈）
```

边界层兜底所有异常，但**错误详情只进日志、不进响应**——内部错误消息可能
包含密钥名、路径、SQL 片段，回显给调用方是信息泄漏。响应体只给"该不该重试"
这个决策所需的最少信息。

## 5. 测试：`_agent()` 工厂是替身注入点

端点经 `api._agent()` 构造 IssueAgent，测试 monkeypatch 它——7 个用例覆盖
401×3（无/错/未配置令牌）、200（响应不含 null 字段）、422、503×2（异常/超时）。
超时测试把 `ANALYSIS_TIMEOUT_SECONDS` patch 成 0.05s + 假 agent 睡 1s——
**时间相关的测试不要等真实时间**。

## 6. 面试自问自答

- Q: 为什么鉴权用依赖注入而不是全局 middleware？ A: 只保护该保护的路由
  （healthz 必须匿名可达），依赖声明在路由上是显式的；middleware 要自己写
  路径过滤，隐式且易漏。
- Q: 422 为什么让 FastAPI 自动生成？ A: Pydantic 模型即合同，校验和文档
  （/docs 的 schema）同源——手写校验反而会和合同漂移。

## 7. 自测证据

`pytest tests/`：27 个测试全绿（合同 13 + Agent 7 + 端点 7）。
