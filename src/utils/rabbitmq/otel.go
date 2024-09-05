package rabbitmq

import (
	"context"
	"go.opentelemetry.io/otel"
)

type AmqpHeadersCarrier map[string]interface{}

func (a AmqpHeadersCarrier) Get(key string) string {
	v, ok := a[key]
	if !ok {
		return ""
	}
	return v.(string)
}

func (a AmqpHeadersCarrier) Set(key string, value string) {
	a[key] = value
}

func (a AmqpHeadersCarrier) Keys() []string {
	i := 0
	r := make([]string, len(a))

	for k := range a {
		r[i] = k
		i++
	}

	return r
}

// InjectAMQPHeaders
// 当前设置的文本映射传播器，并使用 Inject 方法将 ctx 中的跟踪信息注入到 h 中。
// 这些跟踪信息通常包括追踪 ID、Span ID 等
//
//	@Description:
//	@param ctx
//	@return map[string]interface{}
func InjectAMQPHeaders(ctx context.Context) map[string]interface{} {
	h := make(AmqpHeadersCarrier)
	otel.GetTextMapPropagator().Inject(ctx, h)
	return h
}

// ExtractAMQPHeaders
//
//	@Description:
//	@param ctx
//	@param headers
//	@return context.Context  函数返回更新后的 context.Context
func ExtractAMQPHeaders(ctx context.Context, headers map[string]interface{}) context.Context {
	//Extract 方法用于从给定的 headers 中提取跟踪信息，并将其注入到 ctx 中
	//AmqpHeadersCarrier(headers) 将 headers 转换为实现了 OpenTelemetry TextMapCarrier 接口的类型，以便传递给 Extract 方法。
	return otel.GetTextMapPropagator().Extract(ctx, AmqpHeadersCarrier(headers))
	//note：emmmAmqpHeadersCarrier(headers)是类型转换
}
