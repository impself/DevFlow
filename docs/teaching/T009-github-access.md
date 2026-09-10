# T009 教学笔记：GitHub 接入层——控制层第一次"看见"GitHub

> 对应文件：`internal/github/{client,webhook}.go` + `github_test.go`
> 一句话：两块基础设施——**以 App 身份调用 GitHub API**（客户端工厂）与
> **验证 GitHub 的身份**（webhook 验签）。凭据从此被封死在这一个包里（宪法 III）。

> 本任务是「skills + reviewer agent」工作流的第一课：代码完成后由 code-reviewer
> agent 独立审查，2 条 P1（客户端无超时、哨兵错误未按契约断言）+ 6 条 P2 全部修复后才提交。
> 下文 §6 记录了这次审查抓住的真问题。

## 1. GitHub App 的身份模型：谁在调用？

GitHub 有三种调用身份：Personal Access Token（人）、OAuth App（代用户）、**GitHub App（机器身份）**。DevFlow 选 App，因为它的权限模型最细（只申请 Metadata:read / Contents:read / Issues:read+write，宪法 III 最小权限）且以 **installation** 为安装单元。

关键认知：**App 本身不是调用身份，installation 才是**。调用链是：

```
App 私钥 → 签出 JWT（10 分钟有效）→ 换取 installation token（约 1 小时）→ 挂到请求头
```

ghinstallation 把这整条链封装成一个 `http.RoundTripper`——Go 的 `RoundTripper` 接口
是 `http.Client` 的"发动机"：每次请求先经过它。ghinstallation 的实现自动完成
token 换取/过期刷新，业务代码只见普通 client。

## 2. ClientFactory：为什么按 installation ID 缓存

```go
tr := ghinstallation.NewFromAppsTransport(f.appsTransport, installationID)
client, err := githubpkg.NewClient(githubpkg.WithHTTPClient(&http.Client{Transport: tr, Timeout: 30*time.Second}))
```

- **缓存键 = installation_id**，与 repositories 表锚点一致——仓库可以改名换 owner，
  installation 不变；按 owner/name 缓存会在改名后错乱。
- **锁内无 IO**：`sync.Mutex` 只保护 map 读写，token 换取发生在锁外的首次真实调用。
  构造 client 不发网络请求（惰性），测试因此不需要 stub 任何 HTTP。
- **Timeout 30s 是 review 的 P1 产出**：`http.Client` 零值**没有超时**，一次悬挂响应
  能卡死并发上限为 1 的 worker。30s = 租约 TTL 上限，宁可失败重试也不无限等。

> 教学插曲：go-github **v90 改了 `NewClient` 签名**（函数式选项 + 返回 error），
> 网上教程全是最老写法。正确姿势是直接读 `$GOPATH/pkg/mod` 里的模块源码——
> `grep "^func NewClient"` 一分钟解决，比搜索引擎快且绝对准确。

## 3. webhook.go：验签是身份验证，解析是格式转换

### 3.1 顺序即安全：先验签，后解析

```go
if !validateSignature(header.Get("X-Hub-Signature-256"), body, secret) {
    return nil, ErrInvalidSignature
}
event, err := githubpkg.ParseWebHook(eventType, body)
```

不可信的字节在解析之前就该被拒——解析器（JSON decoder）本身也是攻击面。
空 secret 直接报错（fail-fast）：任何攻击者都能对空密钥算出合法 HMAC，
静默放行等于没锁门。

### 3.2 纯函数化：为什么不吃 `*http.Request`

go-github 自带 `ValidatePayload(r, secret)`，但它**吃掉整个 request**——body 读完
就没了，后续落库取证（inbox_events 存原文）要自己再拼。本包收窄为纯函数
`ParseDelivery(secret, header, body)`：输入输出全是值，可单测、无隐藏 IO、
错误语义（`ErrInvalidSignature` 哨兵）在包边界定义。**库函数顺手 ≠ 库函数合适**。

### 3.3 HMAC 验签的三个细节

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write(payload)
hmac.Equal(expected, mac.Sum(nil))  // constant-time
```

- 签名格式 `sha256=<hex>`，先 `strings.HasPrefix` 确认前缀再解码（review P2）；
- `hex.Decode` 失败返回 false 而非 panic——外部输入不可信是默认假设；
- `hmac.Equal` 与 T005 的 `subtle.ConstantTimeCompare` 同一思想：**凡比较秘密，
  禁用短路比较**。

## 4. 测试：不依赖外界的身份层测试

- 测试内**现场生成 RSA 密钥**（`rsa.GenerateKey` + PEM 编码）构造工厂——不需要
  真私钥文件，也不 mock 任何东西；
- 测试内**现场计算 HMAC**（与 GitHub 同算法）——"合法签名"不用猜；
- 断言哨兵用 `errors.Is(err, ErrInvalidSignature)` 而不是匹配错误文本——**消息可以
  改写，契约不能**。这也是 review 抓的第二条 P1：原实现用 `strings.Contains`，
  实现忘了走哨兵路径测试照样绿。

## 5. Review 工作流首秀记录

code-reviewer agent 核实了 ghinstallation 的 token 刷新互斥（读了模块源码）、用
`go test -race` 跑了全部测试、并指出 8 条 P2。已采纳的快赢项：

| 发现 | 处置 |
| --- | --- |
| P1 客户端无超时 | `Timeout: 30s` ✅ |
| P1 哨兵错误未按契约断言 | 改 `errors.Is` ✅ |
| P2 签名前缀未 HasPrefix | 已加 ✅ |
| P2 空 secret 无防御 | 已加 fail-fast ✅ |
| P2 元数据头先于验签（可探测差异） | 已调序 ✅ |
| P2 测试覆盖缺口（缺签名头/未知事件/多 installation） | 已补 3 个用例 ✅ |
| P2 工厂不可注入 mock base URL | 留待集成测试需要时（显式推迟） |
| P2 ghinstallation 内部依赖 v88 与 v90 并存 | 生态常态，记录不动 |

## 6. 自测证据

| 场景 | 结果 |
| --- | --- |
| `go build / vet / gofmt -l` | ✅ |
| ParseDelivery 6 用例（合法/错签/缺头/空secret/缺签名头/未知事件/非法hex） | 全 PASS ✅ |
| ClientFactory（缓存命中/跨 installation 不共享/垃圾 PEM） | PASS ✅ |
| 全仓 `go test ./...`（真实 PG） | ok ✅ |

## 7. 下一站预告

T010 Runner 骨架：worker 池 + 心跳循环 + 优雅停机，把 ClaimRun 查询变成真正在跑的
消费者；随后 Phase 2 Checkpoint 双进程联检。
