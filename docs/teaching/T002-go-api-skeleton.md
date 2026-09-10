# T002 教学笔记：Go API 服务骨架

> 对应任务：`services/control/cmd/api/main.go` + `internal/config/config.go`
> 一句话：给 DevFlow 控制层盖一栋"带门禁和体检报告的办公楼"——进程能启动、能体面退出、
> 配置缺失当场报警、`/healthz` 如实汇报依赖健康。

---

## 1. 这个任务在整栋建筑里的位置

想象控制层是一栋办公楼：

| 建筑 |
------|------
| `main.go` | 总电闸 + 大门：把电（配置）、水（数据库）、门卫（路由）接好 |
| `config.go` | 水电工：开工前逐项检查水电表，缺一样就拒绝开工 |
| `/healthz` | 前台体检屏：每年（每次探测）如实汇报大楼各系统状态 |

后面的 T005（日志/鉴权中间件）、T011（webhook）都是往这栋楼里**搬家具**，楼体本任务浇铸完成。

## 2. 语法点：三个 Go 新手最容易踩的坑

### 2.1 `errors.Is(err, http.ErrServerClosed)`——为什么停机还要判断"错误"？

```go
if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
    serverErr <- err
}
```

`Shutdown()` 优雅停机时，会"温柔地掐断" `ListenAndServe`，后者返回哨兵错误
`http.ErrServerClosed`。这不是故障，是**正常关门的吱呀声**。用 `!=` 直接比较在 Go 里
对包装过的错误（`fmt.Errorf("%w", ...)`）会失效，`errors.Is` 会沿着错误链层层拆包找哨兵——
**判断错误永远用 `errors.Is`/`errors.As`，不要用 `==`**。

### 2.2 `signal.NotifyContext`——把操作系统信号"翻译"成 context 取消

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
```

Ctrl+C 在操作系统层面是信号（Windows 是 console event，Linux 是 SIGINT）。
这个函数把信号接到 `context` 上：信号一到，`ctx.Done()` 关闭，所有监听这个 ctx 的
goroutine（数据库池、HTTP 服务）统一收到"该收摊了"的广播。`defer stop()` 释放信号
处理器，否则信号通道会一直占着。

**为什么用 channel + select 等两种退出？** HTTP 服务异常退出（如端口被占）和用户按
Ctrl+C 是两条独立退出路径，`select` 让"故障退出"和"主动停机"在同一个汇合点处理，
谁先到谁触发。

### 2.3 匿名结构体切片——校验顺序要确定

```go
required := []struct{ name, value string }{ {"DATABASE_URL", ...}, ... }
```

初版用 `map[string]string` 做必填校验，有个隐蔽 bug：**Go 的 map 迭代顺序是故意随机的**
（语言规范写死的，防止你依赖顺序）。后果是缺 3 个变量时每次启动报的错不一样、一次只报一个。
换成匿名结构体切片：顺序 = 声明顺序，且顺手升级成"**一次性列出全部缺失项**"，
操作者一轮就能补齐所有环境变量。

> 教学彩蛋：Go 故意把 map 顺序随机化，是语言设计者对"隐式依赖迭代顺序"的防御。
> Python 的 dict 3.7+ 反而保证插入序——同一问题，两种哲学。

## 3. 架构决策与权衡

### 3.1 `run()` 函数：把 main 的所有路径收进一个 error 通道

```go
func main() { if err := run(); err != nil { slog.Error(...); os.Exit(1) } }
```

Go 的 `main` 没有 defer 兜底（`os.Exit` 会跳过 defer）。把全部逻辑收进 `run()`：
- 失败路径只有一条——`return err`；
- 成功路径的 `defer pool.Close()`、`defer stop()` 全部正常执行。

这是 Go 社区（Mat Ryer 的 "functional options" 作者）推崇的 main 写法：
**main 短得像目录页，真正的剧情在 run() 里**。

### 3.2 依赖注入的小型实践：`server` struct

```go
type server struct { cfg config.Config; pool *pgxpool.Pool }
```

不用全局变量放连接池，而是挂在 struct 上、构造时注入。收益立刻可见：
- 每个 handler 可以带着 mock 依赖单独测试；
- 依赖关系在编译期一目了然（看字段就知道这个服务碰什么）；
- 到 T008/T011 时，`store`、`githubClient` 都往这个 struct 里加字段即可，路由装配处
  是唯一的"接线板"。

放弃的备选：`gin.Default()`。它附赠 Logger + Recovery 两个中间件，但 Logger 是纯文本
格式，与 T005 要做的 `slog` 结构化日志冲突；显式 `gin.New()` + `r.Use(...)` 让每一层
中间件都是自己挑的、可见的。

### 3.3 healthz 为什么要报 503 而不是假装 200？

```go
dbOK := s.pool.Ping(pingCtx) == nil
if !dbOK { code = http.StatusServiceUnavailable; status = "degraded" }
```

健康检查分两种哲学：
- **liveness**（活着吗）：进程能响应就 200——给"要不要重启进程"用的；
- **readiness**（能干活吗）：依赖全绿才 200——给"要不要把流量打过来"用的。

本任务的 `/healthz` 折中：自带依赖状态字段 + 依赖挂时返回 503。**不掩盖依赖故障**
是 PRD §26.3 的硬要求——绿灯骗过的只是自己，K8s 或隧道后面的 GitHub 可不会陪你演。

### 3.4 配置 fail-fast（PRD §29.3）：宁可启动失败，不要带病运行

`Load()` 缺变量直接报错退出，绝不生成默认凭据。对比另一种流派（给默认值"先跑起来再说"）：
配置问题会延迟到**第一次 webhook 进来**才爆炸，而且爆炸现场离根因十万八千里。
fail-fast 把错误从"运行时的深夜"挪到"启动时的白天"。

放弃的备选：
- **viper**：支持文件/远程配置，但引入重依赖；宪法原则 VIII——4 行 `os.Getenv` 能解决的事不请外援；
- **caarlos0/env**：struct tag 反射注入很优雅，但校验逻辑被 tag 字符串藏起来了，报错定制反而绕。

## 4. 数据库连接：`pgxpool` 的"惰性"陷阱

`pgxpool.New()` **不会立刻连数据库**——它只建池子。所以代码里显式 `pool.Ping()` 做启动
探活，否则"数据库配置错了"会潜伏到第一个请求才暴露。Ping 还包了 5 秒超时 context：
启动阶段不无限等待，快速失败。

顺带的 infra 修复：docker-compose 里 PG 18 镜像的数据卷必须挂 `/var/lib/postgresql`
（18 起数据挪进了带版本号的子目录），旧写法 `/var/lib/postgresql/data` 会让容器启动即退。

## 5. 自测证据

| 场景 | 命令 | 期望 | 结果 |
| --- | --- | --- | --- |
| 编译+静态检查 | `go build ./... && go vet ./...` | 无输出 | ✅ |
| 缺配置启动 | 不设任何环境变量 `go run ./cmd/api` | 明确报缺失变量后退出码 1 | ✅ `缺少必需的环境变量: ...` |
| DB 不可达 | `DATABASE_URL` 指向无人监听的 59999 端口 | 报"数据库不可达"退出 | ✅ dial tcp 127.0.0.1:59999 refused |
| `/healthz` 全绿 | 本地 PG 起来后 `curl :8080/healthz` | 200 + `database: true` | ⏳ 留待 Phase 2 Checkpoint（T010）连真库一起验 |

## 6. 下一站预告

T005 会把 `gin.New()` 里预留的中间件位填上：结构化日志、内部令牌鉴权。
T011 的 webhook handler 会挂到这台路由上——今天浇的楼体，马上开始搬家具。
