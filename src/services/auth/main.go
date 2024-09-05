package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/profiling"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/auth"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/redis"
	"GuGoTik/src/utils/consul"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/prom"
	"context"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	redis2 "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"github.com/willf/bloom"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc" //lable：这个库用于对google.golang.org/grpc进行追踪和度量
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"net/http"
	"os"
	"syscall"
)

func main() {
	//step: 配置trace？
	tp, err := tracing.SetTraceProvider(config.AuthRpcServerName)

	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Panicf("Error to set the trace")
	}

	//，TracerProvider 的 Shutdown 方法会被调用。这是一个良好的实践，确保资源在程序结束时得到正确释放，以防止内存泄漏或其他资源管理问题。
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			logging.Logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error to set the trace")
		}
	}()

	// step: Configure Pyroscope
	profiling.InitPyroscope("GuGoTik.AuthService")

	//step:配置服务的log
	log := logging.LogService(config.AuthRpcServerName)

	//step：设置rpc网络监听
	lis, err := net.Listen("tcp", config.EnvCfg.PodIpAddr+config.AuthRpcServerPort)

	if err != nil {
		log.Panicf("Rpc %s listen happens error: %v", config.AuthRpcServerName, err)
	}

	//step：配置普罗米修斯监控grpc服务器的性能指标，指标包括处理时间的直方图指标
	//创建和配置一个 gRPC 服务器的指标收集器,利用 Prometheus 监控 gRPC 服务器的性能
	//srvMetrics 是创建的 gRPC 服务器指标收集器，它包含了处理时间等性能指标
	srvMetrics := grpcprom.NewServerMetrics( //创建一个新的 gRPC 服务器指标收集器
		grpcprom.WithServerHandlingTimeHistogram( //配置服务器处理时间的直方图指标。这个选项会创建一个 Histogram 类型的指标，用于记录和分析服务器处理请求的时间分布。
			//指定直方图的桶（buckets）。直方图的桶定义了数据分布的范围，每个桶表示一个时间区间。
			//在这个例子中，桶的设置包括了从毫秒级别到几分钟的时间范围，以便捕捉不同的处理时间。
			grpcprom.WithHistogramBuckets([]float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}),
		),
	)

	//step: 配置普罗米修斯
	reg := prom.Client //prom.Client 代表 Prometheus 客户端的注册器（Registry）实例,
	// 在 Prometheus 的 Go 客户端库中，Registry 是用于注册和管理所有的指标（Metrics）的组件
	reg.MustRegister(srvMetrics) //将 srvMetrics 注册到 Prometheus 的注册器中。
	//这种方式保证了指标在应用程序运行期间能够被 Prometheus 正确地收集。

	//step：创建布隆过滤器并从数据库初始化
	//Create a new Bloom filter with a target false positive rate of 0.1%
	//这行代码使用了 Bloom 过滤器来创建一个新的 Bloom 过滤器实例。
	//Bloom 过滤器是一种空间效率高的概率数据结构，用于测试一个元素是否属于一个集合。它有一定的误判概率，
	//即可能错误地报告一个元素已经存在，但不会漏掉任何实际存在的元素。
	BloomFilter = bloom.NewWithEstimates(10000000, 0.001) // assuming we have 1 million users
	//10000000：这是 Bloom 过滤器预计会包含的元素数量。
	//0.001：这是 Bloom 过滤器的误判率（false positive rate）。即，过滤器报告一个元素存在的概率。
	//这个值表示过滤器在判断元素是否存在时的错误率。0.001 表示 0.1% 的误判率，即每 1000 个元素中可能会有 1 个元素被错误地认为存在。

	// Initialize BloomFilter from database
	var users []models.User
	userNamesResult := database.Client.WithContext(context.Background()).Select("user_name").Find(&users)
	if userNamesResult.Error != nil {
		log.Panicf("Getting user names from databse happens error: %s", userNamesResult.Error)
		////log.Panicf(...)：使用 log.Panicf 记录错误信息。这会将错误信息输出到日志，并且由于 log.Panicf 会触发 panic，它将中断程序的正常执行。
		panic(userNamesResult.Error)
	}
	for _, u := range users {
		BloomFilter.AddString(u.UserName)
	}

	//step：订阅redis的"GuGoTik-Bloom"频道，实时把username添加到布隆过滤器中
	//Create a go routine to receive redis message and add it to BloomFilter
	//在 Go 中使用 Redis 的发布/订阅（Pub/Sub）功能来异步处理消息
	//代码在一个新的 Goroutine 中启动 Redis 订阅客户端，接收消息，并根据接收到的消息更新 Bloom 过滤器。
	//这里使用了 defer 确保 Redis 订阅通道在结束时正确关闭，并处理可能的错误。
	go func() {
		//订阅 Redis 发布/订阅频道 config.BloomRedisChannel,返回pubSub表示订阅通道
		pubSub := redis.Client.Subscribe(context.Background(), config.BloomRedisChannel)
		defer func(pubSub *redis2.PubSub) {
			err := pubSub.Close()
			if err != nil {
				log.Panicf("Closing redis pubsub happend error: %s", err)
			}
		}(pubSub)

		_, err := pubSub.ReceiveMessage(context.Background())
		if err != nil {
			log.Panicf("Reveiving message from redis happens error: %s", err)
			panic(err)
		}

		ch := pubSub.Channel()
		for msg := range ch {
			log.Infof("Add user name to BloomFilter: %s", msg.Payload)
			BloomFilter.AddString(msg.Payload)
		}
	}()

	//step： 使用 grpc.NewServer 函数创建了一个新的 gRPC 服务器，并为其配置了一些拦截器（interceptors）。
	//这些拦截器用于增强 gRPC 服务器的功能，例如添加分布式追踪和指标监控。
	s := grpc.NewServer( //创建一个新的 gRPC 服务器实例
		//otelgrpc.UnaryServerInterceptor：这是 OpenTelemetry 提供的拦截器，用于在 gRPC 服务器端收集追踪数据。
		grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()), //grpc.UnaryInterceptor这个函数用于添加单个 RPC 调用的拦截器。拦截器可以用于日志记录、监控、认证等。

		//grpc.ChainUnaryInterceptor：这个函数允许你将多个拦截器链接在一起，以便按顺序执行。链中的每个拦截器都会在调用 RPC 方法之前和之后执行。
		//srvMetrics.UnaryServerInterceptor：这似乎是自定义的拦截器，可能用于收集服务器端的度量指标（如请求计数、延迟等）
		//grpcprom.WithExemplarFromContext：这是一个选项函数，用于配置 Prometheus 客户端，使其能够从 gRPC 上下文中提取示例（exemplar），这有助于收集更详细的性能数据。
		grpc.ChainUnaryInterceptor(srvMetrics.UnaryServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))),
		grpc.ChainStreamInterceptor(srvMetrics.StreamServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))),
	)

	//step： 把"GuGoTik-AuthService" 和 端口号 注册到consul
	if err := consul.RegisterConsul(config.AuthRpcServerName, config.AuthRpcServerPort); err != nil {
		log.Panicf("Rpc %s register consul happens error for: %v", config.AuthRpcServerName, err)
	}
	log.Infof("Rpc %s is running at %s now", config.AuthRpcServerName, config.AuthRpcServerPort)

	//step: rpc服务端代码
	var srv AuthServiceImpl                                    //AuthServiceImpl 很可能是一个实现了 auth.AuthServiceServer 接口的结构体，这个接口定义了认证服务的 gRPC 方法。
	auth.RegisterAuthServiceServer(s, srv)                     //注册了认证服务到 gRPC 服务器 s
	grpc_health_v1.RegisterHealthServer(s, health.NewServer()) //注册了健康检查服务到 gRPC 服务器 s

	srv.New()                       //初始化服务的内部状态或资源，主要是其他微服务客户端变量
	srvMetrics.InitializeMetrics(s) //这个方法可能是用于初始化服务的监控指标，例如设置 Prometheus 客户端来收集 gRPC 请求的度量数据。

	//这段代码使用了 oklog/run 包来管理和控制多个并发的服务。
	//run.Group 提供了一种简洁的方式来启动和停止多个 goroutine，并且处理这些 goroutine 的错误和信号

	//step:创建了一个新的 run.Group 实例，用于管理并发任务
	g := &run.Group{}
	//step:添加 gRPC 服务
	g.Add(func() error { //第一个函数用于启动 gRPC 服务器。
		return s.Serve(lis)
	}, func(err error) { //第二个函数用于在出现错误时优雅地停止 gRPC 服务器
		s.GracefulStop()
		s.Stop()
		log.Errorf("Rpc %s listen happens error for: %v", config.AuthRpcServerName, err)
	})

	//step:添加 HTTP 服务，似乎是用来导出metrics的
	httpSrv := &http.Server{Addr: config.EnvCfg.PodIpAddr + config.Metrics}
	g.Add(func() error { //第一个函数用于启动 HTTP 服务器，该服务器用于暴露 Prometheus 指标。
		m := http.NewServeMux()                   //创建一个新的 http.ServeMux 实例，用于路由 HTTP 请求。
		m.Handle("/metrics", promhttp.HandlerFor( //处理函数设置为 Prometheus 的指标处理函数,
			//promhttp.HandlerFor 函数创建一个 HTTP 处理器，用于服务 Prometheus 格式的指标数据。
			reg,
			promhttp.HandlerOpts{
				EnableOpenMetrics: true, //这个选项指示处理器支持 OpenMetrics 格式（Prometheus 指标格式的前身）
			},
		))
		httpSrv.Handler = m //将上面创建的 ServeMux 设置为 HTTP 服务器的处理器
		log.Infof("Promethus now running")
		return httpSrv.ListenAndServe() //启动 HTTP 服务器监听和服务于请求
	}, func(error) { //第二个函数用于在出现错误时优雅地关闭 HTTP 服务器
		if err := httpSrv.Close(); err != nil {
			log.Errorf("Prometheus %s listen happens error for: %v", config.AuthRpcServerName, err)
		}
	})

	//step :添加信号处理
	//这段代码向 Group 中添加了一个信号处理器，当接收到系统信号（如 SIGINT 和 SIGTERM）时，会触发组内所有任务的停止函数。
	//run.SignalHandler 函数（其具体实现未给出，但可以推测其功能）会阻塞直到接收到指定的信号（在这里是 SIGINT 和 SIGTERM）或者context被canceled，然后执行注册的回调函数。
	g.Add(run.SignalHandler(context.Background(), syscall.SIGINT, syscall.SIGTERM))

	//step:调用 g.Run() 开始执行所有添加到 errgroup.Group 中的任务。如果 g.Run() 返回错误，这意味着至少一个任务在执行过程中遇到了问题。
	if err := g.Run(); err != nil {
		log.WithFields(logrus.Fields{ //纪录错误日志和退出
			"err": err,
		}).Errorf("Error when runing http server")
		os.Exit(1)
	}

	//errgroup.Group 是 Go 语言中一个用于并发执行任务并处理它们错误的库。它允许你启动多个任务，并等待它们全部完成或其中一个失败。
	//当任何一个任务返回错误时，errgroup.Group 会取消其他所有任务，并返回遇到的第一个错误。
}
