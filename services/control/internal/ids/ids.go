// Package ids 生成无业务含义的实体 ID。
// 单独成包：controller/publisher 都要用，且测试可能注入假生成器。
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New 返回 "<前缀>-<16字节随机hex>" 形式的 ID，如 run-3f9a2b…。
// 前缀让人在日志/SQL 里一眼认出实体类型；随机部分用 crypto/rand——
// ID 会出现在审批包与外部 URL 场景里，不用可预测的计数器。
func New(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败意味着系统熵源异常，属于不可恢复环境故障
		panic(fmt.Sprintf("ids: 熵源不可用: %v", err))
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
