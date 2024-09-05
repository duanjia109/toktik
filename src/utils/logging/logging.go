// Package logging
// @Description: 实现log和trace的互通？
package logging

import (
	"GuGoTik/src/constant/config"
	"fmt"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"io"
	"os"
	"path"
)

var hostname string

// init
//
//	@Description: 初始化日志记录器，设置日志记录的级别和输出格式，以及配置日志输出的位置
func init() {
	hostname, _ = os.Hostname()

	switch config.EnvCfg.LoggerLevel {
	case "DEBUG":
		log.SetLevel(log.DebugLevel)
	case "INFO":
		log.SetLevel(log.InfoLevel)
	case "WARN", "WARNING":
		log.SetLevel(log.WarnLevel)
	case "ERROR":
		log.SetLevel(log.ErrorLevel)
	case "FATAL":
		log.SetLevel(log.FatalLevel)
	case "TRACE":
		log.SetLevel(log.TraceLevel)
	}

	//step:配置日志输出位置
	filePath := path.Join("/var", "log", "gugotik", "gugotik.log")
	dir := path.Dir(filePath)
	if err := os.MkdirAll(dir, os.FileMode(0755)); err != nil {
		panic(err)
	}

	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	log.SetFormatter(&log.JSONFormatter{})
	//note：表示将一个名为 logTraceHook 的钩子（hook）添加到日志记录器中。这种钩子通常用于在每次记录日志时执行一些额外的操作，比如添加追踪信息、格式化日志消息等。
	log.AddHook(logTraceHook{}) //q：钩子函数接口？
	log.SetOutput(io.MultiWriter(f, os.Stdout))

	Logger = log.WithFields(log.Fields{
		"Tied":     config.EnvCfg.TiedLogging,
		"Hostname": hostname,
		"PodIP":    config.EnvCfg.PodIpAddr,
	})
}

// q：这个是什么呢？
// note：自定义钩子函数，实现Hook接口：
//
//	type Hook interface {
//	   Levels() []Level
//	   Fire(*Entry) error
//	}
type logTraceHook struct{} //自定义钩子函数接口

func (t logTraceHook) Levels() []log.Level { return log.AllLevels }

// Fire
//
//	@Description: 这个函数 Fire 是 logrus 日志库中的一个钩子函数实现，用于在日志条目被记录时执行一些额外的操作。
//
// note:在这个例子中，钩子函数与分布式追踪系统（如 OpenTelemetry）集成，以便在日志中添加追踪信息。
//
// Fire
// Fire() 方法是在记录日志时调用的，它接受一个 logrus.Entry 类型的参数，可以在其中添加额外的字段或执行其他操作。
//
// 在这个结构体的 Fire() 方法中，可以获取当前的追踪信息（如 Trace ID 和 Span ID），并将这些信息添加到日志条目中。
//
//	@Description:在每次记录日志时，该钩子将追踪信息（Trace ID 和 Span ID）添加到日志条目中，并根据配置记录更多的追踪事件。
//	1.在每次记录日志时，检查日志条目是否包含上下文。
//	2.如果有上下文，从中提取追踪信息（Trace ID 和 Span ID），并将这些信息添加到日志条目中。
//	3.根据配置，将日志信息记录为追踪事件，并在发生错误日志时更新追踪状态。
//
//	@receiver t
//	@param entry
//	@return error
func (t logTraceHook) Fire(entry *log.Entry) error {
	ctx := entry.Context
	if ctx == nil {
		return nil
	}

	span := trace.SpanFromContext(ctx)
	//if !span.IsRecording() {
	//	return nil
	//}

	sCtx := span.SpanContext()
	if sCtx.HasTraceID() {
		entry.Data["trace_id"] = sCtx.TraceID().String()
	}
	if sCtx.HasSpanID() {
		entry.Data["span_id"] = sCtx.SpanID().String()
	}

	if config.EnvCfg.LoggerWithTraceState == "enable" {
		attrs := make([]attribute.KeyValue, 0)
		logSeverityKey := attribute.Key("log.severity")
		logMessageKey := attribute.Key("log.message")
		attrs = append(attrs, logSeverityKey.String(entry.Level.String()))
		attrs = append(attrs, logMessageKey.String(entry.Message))
		for key, value := range entry.Data { //把entry.Data中的键值对存到attrs中，形式："log.fields.%s", key - value
			fields := attribute.Key(fmt.Sprintf("log.fields.%s", key))
			attrs = append(attrs, fields.String(fmt.Sprintf("%v", value)))
		}
		span.AddEvent("log", trace.WithAttributes(attrs...))
		if entry.Level <= log.ErrorLevel {
			span.SetStatus(codes.Error, entry.Message)
		}
	}
	return nil
}

var Logger *log.Entry

func LogService(name string) *log.Entry {
	return Logger.WithFields(log.Fields{
		"Service": name,
	})
}

// note：和设置span有关
//
// SetSpanError
//
//	@Description: 当某个操作发生错误时，通过 SetSpanError 函数记录错误和设置状态，可以使得追踪信息更加全面和准确，有助于错误定位和性能分析
//	@param span
//	@param err
func SetSpanError(span trace.Span, err error) {
	span.RecordError(err)                         //这个方法在跨度中记录错误信息。它将 err 这个错误对象记录到 span 中，使得该错误成为追踪信息的一部分。
	span.SetStatus(codes.Error, "Internal Error") //这个方法设置跨度的状态（status）为错误状态
}

// note：和设置span有关
func SetSpanErrorWithDesc(span trace.Span, err error, desc string) {
	span.RecordError(err)
	span.SetStatus(codes.Error, desc)
}

// note： 和设置span有关
func SetSpanWithHostname(span trace.Span) {
	span.SetAttributes(attribute.String("hostname", hostname))
	span.SetAttributes(attribute.String("podIP", config.EnvCfg.PodIpAddr))
}
