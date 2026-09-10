package obs

import (
	"context"
	"log/slog"
)

// InitTracer 初始化链路追踪，返回停机函数。
//
// M1 是占位实现：不注册任何 TracerProvider——OpenTelemetry 的全局默认
// provider 本身就是 no-op（span 数据静默丢弃，API 全部可用），
// 因此「占位」不需要引入 otel 依赖；model_calls.trace_id（T014/T016）
// 用独立生成的 UUID 填充，不依赖这里。
//
// 升级路径（后续里程碑部署 collector 后）：
//  1. go get go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/exporters/otlp/otlptrace
//  2. 在本函数内 sdktrace.New(...) + otel.SetTracerProvider，返回真正的 shutdown；
//  3. 业务代码零改动——这就是把初始化收口到 obs 包的意义（宪法 VII：范围纪律）。
func InitTracer(ctx context.Context, service string) (func(), error) {
	_ = ctx
	shutdown := func() {
		slog.Debug("tracer 关闭（M1 为 no-op 占位）", "service", service)
	}
	return shutdown, nil
}
