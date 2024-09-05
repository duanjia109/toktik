package profiling

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/utils/logging"
	"github.com/pyroscope-io/client/pyroscope"
	log "github.com/sirupsen/logrus"
	"gorm.io/plugin/opentelemetry/logging/logrus"
	"os"
	"runtime"
)

// InitPyroscope
//
//	@Description: 用于初始化和启动 Pyroscope 分析工具的配置和连接
//	@param appName 服务应用的名称
func InitPyroscope(appName string) {
	if config.EnvCfg.PyroscopeState != "enable" { //如果未启动Pyroscope，记录日志并返回
		logging.Logger.WithFields(log.Fields{
			"appName": appName,
		}).Infof("User close Pyroscope, the service would not run.")
		return
	}

	//使用 runtime 包设置了互斥锁和阻塞事件的性能分析参数。
	//这些函数用于在程序运行时收集和记录性能分析数据，例如互斥锁的使用情况和阻塞事件的发生率。
	runtime.SetMutexProfileFraction(5)
	//这个函数的作用是告诉运行时系统在多少比例的互斥操作中收集性能分析数据
	runtime.SetBlockProfileRate(5)

	//step:使用 pyroscope 包的 Start 函数启动 Pyroscope。
	_, err := pyroscope.Start(pyroscope.Config{
		//提供了 pyroscope.Config 结构体作为参数，包括应用程序名称、Pyroscope 服务器地址、日志记录器、标签等配置信息。
		ApplicationName: appName,
		ServerAddress:   config.EnvCfg.PyroscopeAddr,
		Logger:          logrus.NewWriter(),
		Tags:            map[string]string{"hostname": os.Getenv("HOSTNAME")},

		//ProfileTypes 指定了要收集的性能分析类型，包括 CPU 使用情况、内存分配情况、goroutine 数量、互斥锁和阻塞事件的统计等
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,           //分析 CPU 使用率，即程序在执行过程中占用的 CPU 时间。
			pyroscope.ProfileAllocObjects,  //分析程序分配的对象数量，即程序在运行时创建了多少个新对象。
			pyroscope.ProfileAllocSpace,    //分析程序分配的内存空间总量，即程序在运行时请求的总内存字节数。
			pyroscope.ProfileInuseObjects,  //分析当前正在使用的内存中的对象数量，即程序当前持有的对象数量。
			pyroscope.ProfileInuseSpace,    //分析当前正在使用的内存空间量，即程序当前占用的内存字节数。
			pyroscope.ProfileGoroutines,    //分析 Goroutine 的数量，即程序中并发执行的轻量级线程的数量
			pyroscope.ProfileMutexCount,    //分析互斥锁的数量，即程序中使用的互斥锁的总数。
			pyroscope.ProfileMutexDuration, //分析互斥锁的持有时间，即程序中互斥锁被持有的总时间。
			pyroscope.ProfileBlockCount,    //分析阻塞操作的数量，即程序中因为等待互斥锁或其他同步机制而阻塞的总次数。
			pyroscope.ProfileBlockDuration, //分析阻塞操作的持续时间，即程序中因为等待互斥锁或其他同步机制而阻塞的总时间。
		},
	})

	if err != nil {
		logging.Logger.WithFields(log.Fields{
			"appName": appName,
			"err":     err,
		}).Warnf("Pyroscope failed to run.")
		return
	}
}
