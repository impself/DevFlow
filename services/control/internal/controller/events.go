package controller

import (
	"strings"

	githubpkg "github.com/google/go-github/v90/github"
)

// 本文件是事件过滤规则（FR-2 / AC45）的唯一实现。
// 过滤器是 agent 系统的「触发面防线」：多收一件、漏收一件、循环触发，
// 都直接变成模型调用费用或死循环，所以规则必须集中、纯函数、可穷举测试。

// rejection 描述「该事件不进业务流」的决定。
type rejection struct {
	ignore bool
	reason string
}

// isBotSender 判定 sender 是否 bot：type 字段优先，[bot] 后缀兜底。
func isBotSender(u *githubpkg.User) bool {
	if t := u.GetType(); t != "" {
		return t == "Bot"
	}
	return strings.HasSuffix(u.GetLogin(), "[bot]")
}

// decideIssueEvent 判定一个 issues 事件是否触发新的分析 Run。
//
// 纯函数设计：输入事件事实，输出决定——不碰数据库、不碰 HTTP。
// 收益：所有过滤组合能用一张表驱动测试穷举；规则变更零副作用风险。
//
// 规则：
//  1. 只处理 opened / reopened——edited/closed/labeled 等不触发分析
//     （每次编辑都烧一次模型调用，且结论很快过时）；
//  2. bot 发起的事件一律忽略（AC45 防循环）：agent 生态里最经典的
//     事故是「bot 触发 bot」——A bot 的动作产生事件，事件唤醒 B bot，
//     无限互相调用烧穿预算。
//     判定依据（调研 P0-1）：优先用 webhook payload 自带的 sender.type ==
//     "Bot"（零 API 调用，覆盖不以 [bot] 结尾的 App 账号，如 Copilot），
//     [bot] 后缀仅作旧 payload 兜底——名字匹配会漏装任意 slug 的 App。
//  3. 范围外仓库在 Handle 里检查（需要查库，不属于纯函数职责）。
func decideIssueEvent(evt *githubpkg.IssuesEvent) rejection {
	action := evt.GetAction()
	if action != "opened" && action != "reopened" {
		return rejection{ignore: true, reason: "action=" + action}
	}
	if isBotSender(evt.Sender) {
		return rejection{ignore: true, reason: "sender 是 bot，防循环忽略"}
	}
	return rejection{}
}
