// Package logging
// @Description: 实现了gorm框架的日志接口，主要是Trace函数，
// GORM 会自动调用 Trace 接口来记录每个 SQL 查询的详细信息。具体来说，当你使用 GORM 执行数据库操作时，GORM 会在内部调用你实现的日志接口，包括 Trace 方法
package logging

import (
	"context"
	"errors"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
	"time"
)

// focus:这组代码是一个自定义的 GORM 日志记录器，它实现了 GORM 的"loggerl.Interface"接口
// GORM的日志库要求实现以下接口:
//
//	type Interface interface {
//		LogMode(LogLevel) Interface
//		Info(context.Context, string, ...interface{})
//		Warn(context.Context, string, ...interface{})
//		Error(context.Context, string, ...interface{})
//		Trace(context.Context, time.Time, func() (string, int64), error) // 可选
//	}

var errRecordNotFound = errors.New("record not found")

type GormLogger struct{}

func (g GormLogger) LogMode(_ logger.LogLevel) logger.Interface {
	// We do not use this because Gorm will print different log according to log set.
	// However, we just print to TRACE.
	return g
}

// info级别
func (g GormLogger) Info(ctx context.Context, s string, i ...interface{}) {
	//note：使用了 logrus 日志库来记录带有上下文和字段的日志信息
	Logger.WithContext(ctx).WithFields(logrus.Fields{
		"component": "gorm",
	}).Infof(s, i...) //Infof(s, i...): 这方法记录一条信息级别的日志。
}

// warn级别
func (g GormLogger) Warn(ctx context.Context, s string, i ...interface{}) {
	Logger.WithContext(ctx).WithFields(logrus.Fields{
		"component": "gorm",
	}).Warnf(s, i...)
}

// error级别
func (g GormLogger) Error(ctx context.Context, s string, i ...interface{}) {
	Logger.WithContext(ctx).WithFields(logrus.Fields{
		"component": "gorm",
	}).Errorf(s, i...)
}

// Trace
// Trace 函数使用 logrus 记录包含文件名、耗时、行数和 SQL 语句的日志，并且根据是否有错误信息来决定是否记录错误信息。
//
//	@Description: 自定义的日志记录函数，
//	note：明白了，都是自定义了接口的一个实现，虽然是被库函数调用了，不过库里调用的是接口
//	@receiver g
//	@param ctx
//	@param begin
//	@param fc
//	@param err
func (g GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	const traceStr = "File: %s, Cost: %v, Rows: %v, SQL: %s"
	elapsed := time.Since(begin)
	sql, rows := fc()
	fields := logrus.Fields{
		"component": "gorm",
	}
	if err != nil && !errors.Is(err, errRecordNotFound) {
		fields = logrus.Fields{
			"err": err,
		}
	}

	//step： 在这里记录日志
	if rows == -1 {
		Logger.WithContext(ctx).WithFields(fields).Tracef(traceStr, utils.FileWithLineNum(), float64(elapsed.Nanoseconds())/1e6, "-", sql)
	} else {
		Logger.WithContext(ctx).WithFields(fields).Tracef(traceStr, utils.FileWithLineNum(), float64(elapsed.Nanoseconds())/1e6, rows, sql)
	}
}

// note： 自定义了GormLogger类型，实现了库里定义的接口，因此可以自定义日志格式
func GetGormLogger() *GormLogger {
	return &GormLogger{}
}
