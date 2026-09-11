package controller_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/store"
)

// approvalFixture 在 executeFixture 之上完成一次成功分析（产生 ANSWer_READY 草稿）。
func approvalFixture(t *testing.T) (*store.Store, string, string) {
	st, claim1, contents, artifacts, _ := executeFixture(t)
	owner := "worker-exec"
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim1.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, owner, artifacts)
	if _, err := exec.Execute(t.Context(), claim1); err != nil {
		t.Fatalf("执行: %v", err)
	}
	return st, claim1.CaseID, owner
}

func TestCreateBundle(t *testing.T) {
	st, caseID, _ := approvalFixture(t)
	svc := controller.NewApprovalService(st)

	t.Run("从 current 草稿生成审批包：绑定三元组齐全", func(t *testing.T) {
		b, err := svc.CreateBundle(t.Context(), caseID)
		if err != nil {
			t.Fatalf("创建审批包: %v", err)
		}
		if b.Status != "PENDING" || b.ActionType != "POST_ISSUE_COMMENT" {
			t.Fatalf("初始应为 PENDING/POST_ISSUE_COMMENT: %s/%s", b.Status, b.ActionType)
		}
		// target 精确到 numeric_id + issue_number（AC32）——期望值从库里取，不写死
		var wantNumeric, wantIssue int64
		if err := st.Pool().QueryRow(t.Context(), `
			SELECT rep.repo_numeric_id, c.issue_number
			FROM cases c JOIN repositories rep ON rep.id = c.repo_id
			WHERE c.id=$1`, caseID).Scan(&wantNumeric, &wantIssue); err != nil {
			t.Fatal(err)
		}
		// 用 UseNumber 解码：大整数经 float64 会丢精度（JS 消费方的经典坑）
		dec := json.NewDecoder(strings.NewReader(string(b.Target)))
		dec.UseNumber()
		var target map[string]any
		if err := dec.Decode(&target); err != nil {
			t.Fatalf("target 应为 JSON: %v", err)
		}
		gotNum, _ := target["repo_numeric_id"].(json.Number).Int64()
		gotIssue, _ := target["issue_number"].(json.Number).Int64()
		if gotNum != wantNumeric || gotIssue != wantIssue {
			t.Fatalf("target 不符: want %d/%d got %v", wantNumeric, wantIssue, target)
		}
		// content_digest = 草稿 body_digest（AC33 的锚）
		var draftDigest string
		if err := st.Pool().QueryRow(t.Context(),
			`SELECT body_digest FROM reply_drafts WHERE status='current'`).Scan(&draftDigest); err != nil {
			t.Fatal(err)
		}
		if b.ContentDigest != draftDigest {
			t.Fatalf("content_digest 应等于草稿 body_digest")
		}
		// 24h 时效
		if until := b.ExpiresAt.Time.Sub(time.Now()); until < 23*time.Hour || until > 25*time.Hour {
			t.Fatalf("有效期应约 24h，得到 %v", until)
		}
	})

	t.Run("重复创建：幂等回显同一个包（AC35）", func(t *testing.T) {
		svc := controller.NewApprovalService(st)
		b1, err := svc.CreateBundle(t.Context(), caseID)
		if err != nil {
			t.Fatalf("第一次创建: %v", err)
		}
		b2, err := svc.CreateBundle(t.Context(), caseID)
		if err != nil {
			t.Fatalf("第二次创建: %v", err)
		}
		if b1.ID != b2.ID {
			t.Fatalf("同草稿重复创建应返回同一包: %s vs %s", b1.ID, b2.ID)
		}
	})

	t.Run("NEEDS_INFO 草稿不可审批", func(t *testing.T) {
		// 把 current 草稿的结论改成 NEEDS_INFO（补追问字段满足 CHECK/合同）
		if _, err := st.Pool().Exec(t.Context(), `
			UPDATE reply_drafts
			SET conclusion='NEEDS_INFO', needs_info_questions='["x?"]'::jsonb
			WHERE id=(SELECT id FROM reply_drafts WHERE status='current' LIMIT 1)`); err != nil {
			t.Fatalf("改草稿: %v", err)
		}
		if _, err := controller.NewApprovalService(st).CreateBundle(t.Context(), caseID); !errors.Is(err, controller.ErrNotApprovable) {
			t.Fatalf("应 ErrNotApprovable，得到 %v", err)
		}
	})
}
