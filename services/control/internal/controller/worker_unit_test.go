package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateErr 是 P1-3 的回归防线：截断后的文本必须是合法 UTF-8，
// 否则 PG 以 22021 拒收，失败信息永远进不了库。
func TestTruncateErr(t *testing.T) {
	t.Run("短错误原样返回", func(t *testing.T) {
		in := errors.New("短错误")
		if got := truncateErr(in); got != "短错误" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("超长中文按 UTF-8 边界截断", func(t *testing.T) {
		in := errors.New(strings.Repeat("错", 1000)) // 3000 字节，500 落在"错"字中间
		got := truncateErr(in)
		if len(got) >= 500 {
			t.Fatalf("应被截断到 <500 字节，实际 %d", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatal("截断结果必须是合法 UTF-8")
		}
	})

	t.Run("超长英文按字节上限截断", func(t *testing.T) {
		in := errors.New(strings.Repeat("a", 1000))
		if got := truncateErr(in); len(got) != 500 {
			t.Fatalf("ASCII 应精确截到 500 字节，实际 %d", len(got))
		}
	})
}

// TestSafeExecutePanicIsolation：钩子 panic 必须被转成 error，不能带崩 worker。
func TestSafeExecutePanicIsolation(t *testing.T) {
	w := NewWorker(nil, "w-test", func(ctx context.Context, claim Claim) (string, error) {
		panic("boom")
	})
	_, err := w.safeExecute(context.Background(), Claim{ID: "r1"})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panic 应转为 error，得到 %v", err)
	}
}
