package controller

import (
	"testing"

	githubpkg "github.com/google/go-github/v90/github"
)

// mkEvent 构造一个最小 IssuesEvent（type 为空走 [bot] 后缀兜底路径）。
func mkEvent(action, sender string) *githubpkg.IssuesEvent {
	return &githubpkg.IssuesEvent{
		Action: githubpkg.String(action),
		Sender: &githubpkg.User{Login: githubpkg.String(sender)},
	}
}

// mkTypedEvent 显式带 sender.type 的构造（GitHub webhook 实际形态）。
func mkTypedEvent(action, sender, senderType string) *githubpkg.IssuesEvent {
	return &githubpkg.IssuesEvent{
		Action: githubpkg.String(action),
		Sender: &githubpkg.User{Login: githubpkg.String(sender), Type: githubpkg.String(senderType)},
	}
}

// 表驱动穷举过滤规则：规则是纯函数，测试即规格说明书。
func TestDecideIssueEvent(t *testing.T) {
	cases := []struct {
		name       string
		action     string
		sender     string
		senderType string // 空 → mkEvent（[bot] 后缀兜底路径）
		wantIgnore bool
		wantReason string
	}{
		{"opened 人类触发", "opened", "alice", "", false, ""},
		{"reopened 人类触发", "reopened", "alice", "", false, ""},
		{"edited 不处理", "edited", "alice", "", true, "action=edited"},
		{"closed 不处理", "closed", "alice", "", true, "action=closed"},
		{"labeled 不处理", "labeled", "alice", "", true, "action=labeled"},
		{"bot 触发 opened——防循环", "opened", "devflow-bot[bot]", "", true, "sender 是 bot，防循环忽略"},
		{"bot 触发 reopened——防循环", "reopened", "other-bot[bot]", "", true, "sender 是 bot，防循环忽略"},
		{"bot 触发 edited——仍不处理", "edited", "devflow-bot[bot]", "", true, "action=edited"},
		{"空 sender 视为人类", "opened", "", "", false, ""},
		// 以下为 sender.type 维度（调研 P0-1：App 账号可能不以 [bot] 结尾）
		{"type=Bot 拦截（覆盖非 [bot] 命名）", "opened", "copilot-sweeper", "Bot", true, "sender 是 bot，防循环忽略"},
		{"type=User 放行", "opened", "someone", "User", false, ""},
		{"type=Bot 且 reopened 也拦截", "reopened", "x", "Bot", true, "sender 是 bot，防循环忽略"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt := mkEvent(tc.action, tc.sender)
			if tc.senderType != "" {
				evt = mkTypedEvent(tc.action, tc.sender, tc.senderType)
			}
			got := decideIssueEvent(evt)
			if got.ignore != tc.wantIgnore {
				t.Fatalf("ignore = %v, 期望 %v（reason=%q）", got.ignore, tc.wantIgnore, got.reason)
			}
			if tc.wantReason != "" && got.reason != tc.wantReason {
				t.Fatalf("reason = %q, 期望 %q", got.reason, tc.wantReason)
			}
		})
	}
}
