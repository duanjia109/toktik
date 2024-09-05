package tracing

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/utils/logging"
	"context"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"
	trace2 "go.opentelemetry.io/otel/trace"
)

var Tracer trace2.Tracer

// SetTraceProvider
//
//	@Description: 这个函数 SetTraceProvider 是用于初始化 OpenTelemetry 追踪器的。
//	它创建并配置了一个 TracerProvider，包括设置采样策略、导出器和资源属性。
//	@param name
//	@return *trace.TracerProvider
//	@return error
func SetTraceProvider(name string) (*trace.TracerProvider, error) {
	//step：创建一个新的 OTLP Trace 客户端实例
	client := otlptracehttp.NewClient(
		otlptracehttp.WithEndpoint(config.EnvCfg.TracingEndPoint), //设置导出器的端点（Tracing 服务的地址），项目里是jaejer
		otlptracehttp.WithInsecure(),                              //表示不使用安全连接（无 TLS）
	)
	//追踪导出器在分布式追踪系统中是一个关键组件，负责将应用程序收集到的追踪数据（如 span 和 trace）发送到远程的追踪后端服务进行存储和分析。
	//这个过程通常涉及将追踪数据从应用程序导出到如 Jaeger、Zipkin 或其他兼容的追踪系统，以便开发者可以在这些工具中可视化、查询和分析这些追踪数据。
	//step：创建导出器
	exporter, err := otlptrace.New(context.Background(), client) //使用前面创建的客户端，初始化一个新的 OTLP 追踪数据导出器
	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Can not init otel !")
		return nil, err
	}

	//step:配置采样策略
	var sampler trace.Sampler
	if config.EnvCfg.OtelState == "disable" {
		sampler = trace.NeverSample()
	} else {
		sampler = trace.TraceIDRatioBased(config.EnvCfg.OtelSampler) //这里设置为0.1，表示每 10 个请求中，大约只有 1 个请求会被记录和追踪
	}
	//采样是分布式追踪系统中的一个概念，用于控制要收集和记录的追踪数据量。它的作用是减少系统负载和存储需求，尤其是在高流量或大规模分布式系统中，通过有选择地采集部分追踪数据，而不是记录每一个请求。

	//step:创建 TracerProvider
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter), //设置追踪数据的导出方式。这里使用的是批量导出器（Batcher），它将追踪数据收集起来，批量发送到指定的导出器
		trace.WithResource( //设置与追踪相关的资源属性，资源属性通常描述服务的元数据，比如服务名称、版本、部署环境等。
			resource.NewWithAttributes(
				semconv.SchemaURL,                   //表示资源属性的架构 URL，用于描述属性的格式和语义
				semconv.ServiceNameKey.String(name), //设置服务的名称
			),
		),
		trace.WithSampler(sampler), //设置采样策略
	)
	//这段代码是在配置和创建一个 TracerProvider，用于管理和生成分布式追踪中的 tracer 实例。
	//TracerProvider 是 OpenTelemetry 中的一个核心组件，负责管理整个应用程序的追踪行为，包括如何采样、导出和记录追踪数据。

	//step:将创建的 TracerProvider 设置为全局的 TracerProvider。
	//设置全局 TracerProvider 意味着你将为整个应用程序指定一个统一的追踪管理方式，所有创建的 tracer 都将使用这个 TracerProvider 的配置。
	//一旦设置了全局的 TracerProvider，应用程序中任何地方
	//使用 otel.Tracer() 获取的 Tracer 都会使用这个 TracerProvider。
	otel.SetTracerProvider(tp)

	//设置全局上下文传播器（Text Map Propagator），这里使用了组合传播器，包括 TraceContext 和 Baggage。
	//上下文传播器在分布式追踪中扮演了关键角色，它负责在不同服务之间传递和提取追踪上下文信息，使得跨服务的追踪成为可能。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	//step:初始化全局 Tracer
	Tracer = otel.Tracer(name) //获取一个名为 name 的全局 Tracer，用于创建和管理跨度。
	return tp, nil
}
