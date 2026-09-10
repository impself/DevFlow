// Package obs 统一收口横切的可观测性设施：结构化日志与链路追踪初始化。
// 服务代码不自己创建 handler，一律经本包取得 logger——
// 将来改输出格式/加采样只动这里，不动业务代码。
package obs

import (
	"log/slog"
	"os"
	"strings"
)

// Setup 创建 JSON 结构化 logger 并设为 slog 默认值。
// 之后任何代码（含依赖库）调用 slog.Info* 都走同一出口，格式统一、字段可查询。
//
// 设计取向：输出到 stdout（12-factor：日志是事件流，收集归部署环境管）；
// JSON 格式（字段化，后续可直投 Loki/ELK）；级别由 LOG_LEVEL 环境变量控制。
func Setup(service string) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

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

// parseLevel 解析 LOG_LEVEL；未设置或非法时取 Info（生产默认不宜 Debug）。
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
