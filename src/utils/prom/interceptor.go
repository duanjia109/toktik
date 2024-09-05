// Package prom
// @Description: metrics和traceid的互联互通
package prom

import (
	"GuGoTik/src/constant/config"
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

// ExtractContext
// 函数在这里的用途是作为 gRPC 服务器端的拦截器的一部分，用于从 gRPC 请求的上下文（context.Context）中提取特定的追踪和度量信息，
// 并将这些信息作为 Prometheus 的标签（prometheus.Labels）返回。这些标签随后可以用于 Prometheus 的度量数据收集，
// 特别是在收集具有详细上下文信息的度量数据时非常有用。
// 通过这种方式，ExtractContext 函数有助于在 gRPC 服务的性能监控和追踪中提供上下文感知的度量数据，
// 使得开发者能够更好地理解服务的行为，诊断问题，并优化性能。
//
//	@Description: 从给定的 context.Context 中提取信息，并返回一个 prometheus.Labels 类型的对象，用于监控或度量系统
//	@param ctx
//	@return prometheus.Labels
func ExtractContext(ctx context.Context) prometheus.Labels {
	if span := trace.SpanContextFromContext(ctx); span.IsSampled() { //函数尝试从 gRPC 上下文中提取 OpenTelemetry 的追踪跨度（span）。
		// 如果存在有效的追踪跨度，并且该跨度已被标记为采样（span.IsSampled() 返回 true），则继续执行
		return prometheus.Labels{
			"traceID": span.TraceID().String(),
			"spanID":  span.SpanID().String(),
			"podId":   config.EnvCfg.PodIpAddr,
		}
	}
	return nil
}
