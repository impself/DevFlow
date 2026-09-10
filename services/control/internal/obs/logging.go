// Package obs 统一收口横切的可观测性设施：结构化日志与链路追踪初始化。
// 服务代码不自己创建 handler，一律经本包取得 logger——
// 将来改输出格式/加采样只动这里，不动业务代码。
package obs

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Setup 创建 JSON 结构化 logger 并设为 slog 默认值。
// 之后任何代码（含依赖库）调用 slog.Info* 都走同一出口，格式统一、字段可查询。
//
// 设计取向：输出到 stdout（12-factor：日志是事件流，收集归部署环境管）；
// JSON 格式（字段化，后续可直投 Loki/ELK）；级别由 LOG_LEVEL 环境变量控制。
//
// 豁免说明：本函数直接读环境变量，是 config.go「组件不读环境变量」原则的
// 引导例外——logger 是所有组件的地基，必须先于 config 存在。
func Setup(service string) *slog.Logger {
	level, levelSource := parseLevel(os.Getenv("LOG_LEVEL"))
	if levelSource == "default-invalid" {
		// 此时 slog 默认 logger 尚未装配，用标准库直接向 stderr 喊一次：
		// 配置拼写错误（WARN/inof…）不该静默吞掉。
		fmt.Fprintf(os.Stderr, "警告: LOG_LEVEL 值无法识别，已回退 info\n")
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(handler).With(
		slog.String("service", service),
	)
	// 让 slog 全局默认值也指向它：库代码里裸调 slog.Info 也归队。
	slog.SetDefault(logger)
	return logger
}

// parseLevel 解析 LOG_LEVEL；未设置取 Info（生产默认不宜 Debug），
// 设置了但非法同样回落 Info，由调用方借第二个返回值决定是否告警。
func parseLevel(s string) (slog.Level, string) {
	switch strings.ToLower(s) {
	case "":
		return slog.LevelInfo, "unset"
	case "debug":
		return slog.LevelDebug, "ok"
	case "info":
		return slog.LevelInfo, "ok"
	case "warn":
		return slog.LevelWarn, "ok"
	case "error":
		return slog.LevelError, "ok"
	default:
		return slog.LevelInfo, "default-invalid"
	}
}
