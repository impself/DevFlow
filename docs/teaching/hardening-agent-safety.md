# 调研驱动硬化：防循环升级 + 引用逐字门（面试高价值篇）

> 对应改动：`events.go`（sender.type 判 bot）、`execute.go`（gateEvidence 逐字门）、
> `0002` 迁移（attempts 预算）、心跳 status 守卫
> 来源：两轮 opensource-scout 调研（官方源码级验证 + 2026 论文），
> 全部对照归档在 [research-alignment.md](./research-alignment.md)。

## 1. bot 判定：从名字匹配升级到类型判定

**旧实现**：`strings.HasSuffix(login, "[bot]")`。
**调研发现**（claude-code-action `actor.ts` 源码级验证）：GitHub App 安装可配置
**任意 slug**——Copilot 就是不以 `[bot]` 结尾的现成反例，Anthropic 官方在
注释里专门点名它。pr-agent 的做法是读 webhook payload 自带的
`sender.type == "Bot"`——**零额外 API 调用，类型字段比名字可靠**。

```go
func isBotSender(u *githubpkg.User) bool {
    if t := u.GetType(); t != "" {
        return t == "Bot"     // payload 自带类型，覆盖任意 slug 的 App
    }
    return strings.HasSuffix(u.GetLogin(), "[bot]") // 旧 payload 兜底
}
```

面试讲法：*「防循环判定我从名字后缀升级成了 payload 的类型字段——依据是
Anthropic 官方 action 的源码，它们用 Users API 的 type 判非人类，注释里点名
Copilot 这种不以 [bot] 结尾的 App 账号。名字是约定，类型是承诺。」*

## 2. 引用逐字门（verbatim gate）：0 token 的反幻觉防线

**问题**：模型给出的 evidence（quote+行号）「可机械核对」——但谁来核对？
此前答案是"审批时人肉核对"。调研（Gemini cookbook Citation Faithfulness
Check）给出了更强的立场：**必须机器执行**，且分级：

```
第一级（0 token，纯代码，fail-closed）：quote 空白归一后逐字出现在文件里？
                                          行号框得住 quote？
第二级（贵）：支持性判断——"quote 真的支持结论吗"
```

关键设计原则（Gemini notebook 原文精神）：
- **"fabrications never reach the judge, so they cost zero tokens"**——编造的
  引用在第一级就被杀掉，一分钱不花；
- **fail-closed**：空 quote、缺文件、行号越界，一律不认（宁可错杀，不可放过）；
- **存在性 ≠ 支持性**（"A found citation is not yet correct"）——第二级恰好是
  我们的人工审批门。系统分层：机器管存在性，人管支持性。

我们的 `gateEvidence` 实现（三类失败全覆盖）：

| 失败类型 | 定义（Gemini 术语） | 检测手段 |
| --- | --- | --- |
| Fabricated | quote 在任何文件里都不存在 | 归一化 substring 全文匹配 |
| Frankenquote | 每个词都真实但从未连续出现 | 同上（substring 保证连续性） |
| Misattributed（变体） | quote 真实但行号错 | 行区间内再做一次 substring |

拦截后果：剔除该条 evidence；**ANSWER_READY 失去全部证据 → 降级 NEEDS_INFO**
——对齐 Google Sufficient Context（ICLR 2025）的结论：*强模型在上下文不足时
倾向硬答而非弃权，selective generation/abstention 能提升正确率*。我们的
降级就是 abstention 的工程化。

实现细节两个值得讲的点：
- **空白归一**（`strings.Fields` 折叠）：容忍换行/缩进差异，不容忍内容差异——
  这是比对的容差下限，抄自 Gemini notebook 的 normalize；
- **提交规范化结果**：门修改后的输出（降级/剔证据）才是提交进 run_commits 的
  result；原始模型输出在 raw_model_io artifact 留档——审计能同时看到"模型说了
  什么"和"系统认可了什么"。

## 3. 毒 run 预算：epoch 的盲区

调研（pg-boss/Graphile 源码对比）指出：**epoch 只防双主，不防毒任务**——
反复崩溃的 run 每次 RECOVERING 重领只是 epoch+1，无限循环。修复（0002 迁移）：
- `attempts` 领取时 +1，`attempts >= max_attempts(3)` 不可再领取；
- Sweeper 两段式：先翻 RECOVERING，再终结耗尽预算的（FAILED + 原因入 error）。

对齐语义：pg-boss 的 `retry_limit` + dead letter、Graphile 的领取时 attempts+1
（"Failed forever"）。

## 4. 心跳守卫：一个微妙的竞态

清道夫把过期 run 翻成 RECOVERING 后，**迟到的心跳**（owner/epoch 都还没变，
因为没人接管）若续租成功，会把任务"复活"回执行态——与恢复机制赛跑。
修复：心跳 WHERE 补 `status='RUNNING'`——翻转与续租靠行锁 + 双方守卫原子分胜负。
这正是分布式协议调研里"两侧 WHERE 都必须带完整守卫"的具体化。

## 5. 面试自问自答

- Q: 为什么不在 Python 侧做逐字门？ A: 信任边界——门是控制层对智能层的复验
  （与合同校验同层），Python 侧做了不能替代 Go 侧做（宪法 IV：Go 是权威）。
  且 0 token 的 Go 实现不增加任何模型成本。
- Q: 逐字门会不会错杀意译引用？ A: 会，这是刻意的 fail-closed——错杀的代价
  是降级转人工，错放的代价是带假引用的 ANSWER_READY 进审批流。不对称的后果
  决定不对称的阈值。
- Q: 预算为什么是 3 次而不是更多？ A: 崩溃恢复场景下 3 次已覆盖绝大多数瞬时
  故障；每次重领都会重跑整个分析（真金白银），与模型调用预算（24 次）同哲学：
  有界预算 + 显式终态，绝不无限重试。

## 6. 自测证据

- 过滤测试 12 用例（含 sender.type 维度：Bot 拦截/User 放行/非 [bot] 命名）；
- 逐字门 5 用例（合法通过/编造降级/行号越界降级/空白容差/原生 NEEDS_INFO 不误伤）；
- 毒 run 预算 + 心跳守卫各 1 用例；全套 `go test ./...` 绿。
