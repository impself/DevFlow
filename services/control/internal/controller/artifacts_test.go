package controller_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impself/DevFlow/services/control/internal/controller"
)

// TestArtifacts 落库链路：文件按 sha256 命名落盘、artifacts/reply_drafts 行齐、
// 同 case 第二次分析后旧草稿翻 superseded。
func TestArtifacts(t *testing.T) {
	// executeFixture 自带独立临时库、种 run 并以 worker-exec 领取（epoch=1）
	st, claim1, contents, artifacts, artifactDir := executeFixture(t)
	pool := st.Pool()
	owner := "worker-exec" // 必须与夹具领取身份一致，否则提交会被 epoch fencing 拒绝
	analyzer := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim1.ID, 1))}
	exec := controller.NewRunExecutor(st, contents, analyzer, owner, artifacts)
	if _, err := exec.Execute(t.Context(), claim1); err != nil {
		t.Fatalf("第一次执行: %v", err)
	}

	// 草稿 current 恰一条，结论 ANSWER_READY
	var conclusion, status string
	if err := pool.QueryRow(t.Context(),
		`SELECT conclusion, status FROM reply_drafts WHERE status='current' LIMIT 1`,
	).Scan(&conclusion, &status); err != nil || conclusion != "ANSWER_READY" {
		t.Fatalf("应有 current 的 ANSWER_READY 草稿: %s %s err=%v", conclusion, status, err)
	}

	// 三个 artifact（正文/证据/原始输出），每个文件都在盘上、大小与 size_bytes 一致
	if got := count(t, pool, `SELECT count(*) FROM artifacts`); got != 3 {
		t.Fatalf("应有 3 个 artifact，得到 %d", got)
	}
	rows, err := pool.Query(t.Context(), `SELECT content_digest, size_bytes FROM artifacts`)
	if err != nil {
		t.Fatalf("查询 artifacts: %v", err)
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		var d string
		var size int
		if err := rows.Scan(&d, &size); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, d)
		data, err := os.ReadFile(filepath.Join(artifactDir, d[:2], d))
		if err != nil {
			t.Fatalf("产物文件应存在: %v", err)
		}
		if len(data) != size {
			t.Fatalf("size_bytes 应与文件一致: %d vs %d", len(data), size)
		}
	}
	if len(digests) != 3 {
		t.Fatalf("应扫到 3 个 digest，得到 %d", len(digests))
	}

	// 第二次分析（新 run，同 case）：旧草稿 superseded，新草稿 current
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO inbox_events (id, delivery_id, event_type, payload, signature_valid)
		VALUES ('ev-art2', 'd-art2', 'issues', '{}', true)`); err != nil {
		t.Fatalf("seed event2: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
		VALUES ('run-art2', (SELECT case_id FROM runs WHERE id=$1), 'ev-art2', '{}')`, claim1.ID); err != nil {
		t.Fatalf("seed run2: %v", err)
	}
	claim2, err := st.ClaimRun(t.Context(), &owner)
	if err != nil {
		t.Fatalf("领取 run2: %v", err)
	}
	analyzer2 := &fakeAnalyzer{resp: []byte(strings.Replace(analyzerAnswerReady, "__RUN__", claim2.ID, 1))}
	exec2 := controller.NewRunExecutor(st, contents, analyzer2, owner, artifacts)
	if _, err := exec2.Execute(t.Context(), claim2); err != nil {
		t.Fatalf("第二次执行: %v", err)
	}

	if got := count(t, pool, `SELECT count(*) FROM reply_drafts WHERE status='superseded'`); got != 1 {
		t.Fatalf("旧草稿应翻 superseded，得到 %d", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM reply_drafts WHERE status='current'`); got != 1 {
		t.Fatalf("current 草稿应恰好一条，得到 %d", got)
	}
	var currentRun string
	if err := pool.QueryRow(t.Context(),
		`SELECT run_id FROM reply_drafts WHERE status='current'`).Scan(&currentRun); err != nil || currentRun != claim2.ID {
		t.Fatalf("current 应属于第二次 run: %s err=%v", currentRun, err)
	}
}
