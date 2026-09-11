# T015 教学笔记：Python 合同镜像——跨语言的双向校验

> 对应文件：`app/contracts/models.py`（Pydantic 镜像）、`tests/contract/test_issue_agent_output.py`
> 面试视角：**跨语言合同的工程化**——镜像漂移检测 + 条件规则的两侧实现。

## 1. 为什么 schema 之外还要有 Pydantic 模型？

三层校验各司其职：

| 层 | 技术 | 挡住什么 |
| --- | --- | --- |
| 智能层出口 | Pydantic `model_validator` | LLM 结构化输出不合规则（**重试/降级的判据**） |
| 传输 | Go `contract` 包内嵌 schema 复验 | 版本错配/实现漂移 |
| 测试 | jsonschema 双向校验 | **镜像漂移**（本任务的核心） |

Pydantic 模型不是 schema 的重复——它是**编程语言侧的使用接口**（属性访问、IDE
补全、类型提示），同时承担 T016 的重试判据：`model_validate` 抛错 = 模型输出
不合格 → 重试 → 降级。

## 2. 双向校验：镜像漂移的自动检测

```python
# 方向一：schema 自带示例 → jsonschema 校验（schema 自洽）
# 方向二：Pydantic 实例 → model_dump_json → jsonschema 校验（镜像不漂移）
# 方向三：schema 示例 → Pydantic 解析 → round trip 相等
```

任何一侧单独改动（加字段、改枚举、收紧规则）都会让另一侧的断言爆炸。
这条测试的价值在第一次改动合同时兑现——**合同的生命周期里，改合同的人
往往不是写镜像的人**。

## 3. 实战抓到的漂移：null vs 缺省

测试当场抓到一个真 bug：Pydantic 把 `needs_info_questions=None` 序列化成
`"needs_info_questions": null`，但 schema 里该字段**没有 nullable**——null
过不了 Go 侧复验。JSON 语义上「null」和「字段不存在」是两回事：

```python
def model_dump_json(self, **kwargs) -> str:
    kwargs.setdefault("exclude_none", True)  # None 字段必须缺省
    return super().model_dump_json(**kwargs)
```

修在**序列化出口统一处理**而不是要求每个调用方记得 `exclude_none=True`——
容易被忘记的约定要变成结构保证。

## 4. 条件规则的三处同步点

ANSWER_READY 的规则（必须有 reply+evidence、degraded 不得 READY）存在于：
1. schema 的 `allOf/if-then`（唯一真源）；
2. Pydantic 的 `model_validator`（运行时判据）——注释互引；
3. 两边测试的用例（回归防线）。

改动合同必须三处同步——这是**有意的冗余**（校验是安全关键路径，
单点实现意味着单点失效），测试保证冗余不腐化。

## 5. Pydantic v2 语法点

- `Literal["doc_file", "code_file"]`：枚举字面量，比 Enum 类轻；
- `Field(pattern=r"^[0-9a-f]{7,40}$")`：正则约束直译 schema 的 pattern；
- `model_validator(mode="after")`：跨字段校验钩子，`self` 已构造完成；
- `Model | None` 联合类型：现代注解写法，3.10+ 原生。

## 6. 自测证据

13 个测试：schema 自洽 3 + 镜像不漂移 3 + 条件规则 7，全部通过。

## 7. 下一站预告

T016 Issue Agent 用这套模型做重试判据：AgentScope 结构化输出 → Pydantic 校验
失败 → 重试 1-2 次 → 降级 NEEDS_INFO（degraded=true）。
