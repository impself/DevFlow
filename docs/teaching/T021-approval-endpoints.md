# T021 教学笔记：批准/拒绝端点——状态码即状态机

> 对应文件：`internal/controller/approval_handler.go`、`internal/middleware`（泛化 TokenAuth）、
> `cmd/api` 路由、`approval_handler_test.go`
> 面试视角：**HTTP 层薄、领域层厚**的分层实践 + 三个 AC 在端点上的映射。

## 1. 分层：handler 只做翻译，不做决策

```
Service（approval.go）          Handler（本文件）
  领域错误                        HTTP 状态码
  ErrBundleExpired      ──→      409 + EXPIRED 状态
  ErrBundleNotState     ──→      批准=200 回显 / 拒绝=409
  ErrNoDraft            ──→      404
  store.ErrNoRows       ──→      404
  其他                  ──→      500（详情只进日志）
```

收益直接体现在测试上：服务层的 6 个测试**不需要 HTTP**（纯函数级），
handler 的 4 个测试只验「错误→状态码」的翻译表。业务规则变更不碰 HTTP 层，
API 契约变更不碰业务层。

## 2. AC32/33/35 在同一个端点上的会师

- **AC32（服务端认证）**：路由组挂 `OperatorAuth` 中间件——身份由服务端持有的
  令牌决定，请求体里声称的"我是谁"一概不看。M1 单操作者 = 一把共享令牌，
  多操作者时这里换成真正的会话/JWT，路由不变。
- **AC33（过期/漂移 → 409）**：批准前核对 content_digest 仍等于 current 草稿的
  body_digest，不匹配就地翻 EXPIRED 并返回 409。
- **AC35（重复批准回显）**：包已在 APPROVED/EXECUTING/EXECUTED → 直接 200 返回
  当前状态。注意同一个 `ErrBundleNotState` 在 approve/reject 两个端点的语义不同：
  批准的重复是**幂等成功**（200），拒绝已终态包是**冲突**（409）——
  错误类型相同，HTTP 语义按操作意图翻译。

## 3. 中间件泛化：一次抽象，两个场景

原 `InternalTokenAuth` 泛化为 `TokenAuth(header, token)`，派生两个具名包装：

```go
InternalTokenAuth(token)  // X-DevFlow-Internal-Token，服务间
OperatorAuth(token)       // X-Operator-Token，操作者 API
```

抽象的时机值得注意：**第一次出现重复征兆时才泛化**（T005 只有一个中间件时
不抽象，T021 出现第二个场景才提炼）——提前抽象是猜测，这次是确认。

## 4. 测试工具：`between` 的取舍

测试用 `strings.Index` 手工抠 JSON 字段而不是引 JSON 库——轻量但脆弱
（字段顺序变化即失效）。可接受的取舍：测试断言的字段（id/status）极其稳定，
且标准库方案零依赖。**测试工具代码也要做依赖决策，但标准可以比生产代码松**。

## 5. 面试自问自答

- Q: 为什么 approve 的并发窗口（核对通过后被他人改态）返回 ErrBundleNotState
  而不是重试？ A: 那个窗口意味着"有并发操作者或状态已变"，重试只会重复冲突；
  返回 409 让操作者刷新看最新状态，是人工审批流的正确语义。
- Q: 为什么 route group 用 `/api` 前缀？ A: 与 Vite dev proxy 的 `/api` 转发
  约定对齐（T004），前端所有请求都是同源相对路径——环境差异被配置吸收。

## 6. 自测证据

4 个端点子测试（401/200 批准/200 重复回显/409 终态拒绝）+ 服务层 3 个测试，
全套 `go test ./...` 绿。
