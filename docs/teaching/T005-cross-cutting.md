# T005 教学笔记：横切设施（日志 / 内部鉴权中间件 / 追踪占位）

> 对应文件：`internal/obs/{logging,tracing}.go`、`internal/middleware/internal_token.go`（+测试）、
> `cmd/api/main.go` 装配改动
> 一句话：装三样"每一层楼都要用"的公共设施——广播系统（日志）、门禁卡（内部令牌）、
> 预留的监控管线（追踪），并立规矩：**横切关注点必须收口，不许散装**。

## 1. 横切关注点：为什么它们值得一个独立包

日志、鉴权、追踪的共性：**不属于任何业务模块，却被所有业务模块需要**。两种组织法：

- 散装：每个 handler 自己开 logger、自己读 header 校验——格式漂移、校验不一致、
  改一处要动 N 处；
- 收口（本任务）：`internal/obs` 管日志追踪，`internal/middleware` 管中间件，
  业务代码只"取用"。

Go 的 `internal/` 目录本身就在编译期保证私有性：仓外模块 import 不了它——横切设施
是这栋楼的**承重结构**，不对外出租。

## 2. logging.go：`log/slog` 与"默认值劫持"

### 2.1 结构化日志 vs 文本日志

```
传统：  2026/09/10 23:03:38 api 服务退出 err=...
slog：  {"time":"...","level":"ERROR","msg":"api 服务退出","err":"...","service":"devflow-api"}
```

文本日志给人看；JSON 日志给**机器**看——字段可直接进 Loki/ELK 做查询
（`service=devflow-api AND level=ERROR`）。PRD §26 要求可观测性，第一块基石就是日志
可查询。`slog` 是 Go 1.21+ 标准库——零依赖（宪法 VIII 的"先查标准库"）。

### 2.2 `slog.SetDefault(logger)`：一次注入，全局生效

```go
logger := slog.New(handler).With(slog.String("service", service))
slog.SetDefault(logger)
```

`.With()` 返回带预置字段的新 logger——每行日志自动携带 `service` 标签，调用方无感。
`SetDefault` 让所有裸调 `slog.Info(...)` 的代码（包括第三方库）都流进这个 handler。
这是**依赖注入的变体**：显式传参当然更"纯"，但日志是横切关注点，全局默认值 +
集中装配是 Go 社区的主流折中。

### 2.3 级别解析的 default 分支

`parseLevel` 对未知值回落 `Info` 而不是报错——配置容错有个不对称原则：
**写错的日志级别只损失精度，启动失败的代价却是不必要的中断**。
对比 T002 的数据库配置缺失即退出：那里的错误"拖着只会更糟"，这里"将错就错无害"。
fail-fast 不是教条，是按错误后果定价。

## 3. internal_token.go：门禁卡的三个安全细节

### 3.1 `crypto/subtle.ConstantTimeCompare`——防计时攻击

普通的 `==` 比较字符串在第一个不匹配字节就返回，**耗时正比于匹配前缀长度**。
攻击者高频探测可测出"前几位是对的"，逐字节猜出令牌。constant-time 比较
无论差在哪都花同样时间，这条信息通道被焊死。安全代码的惯性：
**凡比较密钥/摘要，用 subtle 包，不用 `==`**。

### 3.2 `c.AbortWithStatusJSON` vs `c.JSON`

Gin 中间件链上，`c.JSON` 只是写响应，**链会继续走**（handler 照样执行）；
`Abort` 才会把链拦腰截断。忘了 Abort = 门禁卡刷不开但门照样开。
401 响应不区分"没带令牌"和"带错令牌"——不给探测者反馈哪步错了。

### 3.3 测试即规格：表驱动 + 三种身份

```go
cases := []struct{ name, header string; want int }{
    {"无令牌", "", 401}, {"错误令牌", "wrong", 401}, {"正确令牌", token, 200},
}
```

表驱动测试是 Go 测试的招牌模式：加一个用例=加一行结构体。三个用例恰好是中间件的
完整规格说明。`httptest.NewRecorder()` 充当假 ResponseWriter——不发真端口，
毫秒级跑完（本任务实测 0.3s 内含编译）。

## 4. tracing.go：一个"诚实的占位"是怎么写的

`InitTracer` 当前什么都不做（otel 全局默认 provider 本身就是 no-op），注释里写明：
- **为什么 M1 占位**：没有 collector 可部署，真链路数据无处可去（宪法 VII 范围纪律）；
- **升级路径三步**：装 SDK → 本函数内换实现 → 业务零改动；
- **不牵连**：model_calls 的 trace_id 用独立 UUID，不等追踪系统。

占位代码最大的风险是"占位变成永久"且没人知道为什么。**注释里写清升级条件**，
占位就成了显式决策而非欠账。

> 工程插曲：原计划引 `go.opentelemetry.io/otel/noop`（两个纯接口小模块），但 git 直连
> GitHub 被网络代理拦断、模块代理上查无此模块——遂退回零依赖占位。依赖越少，
> 构建对网络越免疫；这也是「宪法 VIII」在微观处的体现。

## 5. main.go 装配：顺序即语义

```go
obs.Setup("devflow-api")     // 第一件事：日志先就位
cfg, err := config.Load()    // 配置报错已是 JSON 格式
...
shutdownTracer, _ := obs.InitTracer(ctx, ...); defer shutdownTracer()
```

横切设施的初始化顺序有讲究：**日志最先**（后面所有步骤的错误都要靠它记录）；
追踪在 config 之后（追踪初始化可能需要配置）。defer 顺序 = 装配顺序的镜像，
停机时先关追踪再关日志，方向天然正确。

## 6. 自测证据

| 场景 | 结果 |
| --- | --- |
| `go build ./... && go vet ./...` | ✅ |
| `go test ./internal/middleware/` | 3 个身份用例（无/错/对 → 401/401/200）全部通过 ✅ |
| main 装配后 JSON 日志输出 | 构建通过，实际输出留待 Phase 2 Checkpoint 与服务联检 ✅ |

## 7. 下一站预告

Phase 2 正式开工：T006 建 10 张表、T007 sqlc 查询、T008 Store 封装——
Go 开始真正"住进"数据库；本地 PG 容器将在 Checkpoint 前就位。
