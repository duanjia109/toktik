package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/profiling"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/feed"
	"GuGoTik/src/utils/consul"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/prom"
	"context"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"net/http"
	"os"
	"syscall"
)

func main() {
	tp, err := tracing.SetTraceProvider(config.FeedRpcServerName)

	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Panicf("Error to set the trace")
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			logging.Logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error to set the trace")
		}
	}()

	// Configure Pyroscope
	profiling.InitPyroscope("GuGoTik.FeedService")

	log := logging.LogService(config.FeedRpcServerName)
	lis, err := net.Listen("tcp", config.EnvCfg.PodIpAddr+config.FeedRpcServerPort)

	if err != nil {
		log.Panicf("Rpc %s listen happens error: %v", config.FeedRpcServerName, err)
	}

	//注意：这段代码是用于在gRPC服务器中集成Prometheus度量指标的。它配置并注册了服务器的度量指标，以便可以监控服务器的性能和请求处理时间。
	//srvMetrics是一个 gRPC 服务器的指标收集器，专门用于收集和记录 gRPC 服务器的性能指标，特别是与 Prometheus 监控系统集成。
	//通过定义直方图桶（buckets）， 可以详细记录请求处理时间的分布情况，并将这些指标注册到Prometheus客户端注册表中，方便Prometheus收集和可视化这些数据。
	srvMetrics := grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}),
		),
	)
	reg := prom.Client
	reg.MustRegister(srvMetrics)

	//这段代码展示了如何使用gRPC和OpenTelemetry、Prometheus集成以监控和追踪gRPC服务器的性能。
	//创建了一个新的gRPC服务器实例，并配置了拦截器（interceptors）以实现这些功能
	//lable:这种方式可以将Prometheus的性能度量数据与OpenTelemetry的追踪信息（Trace ID 和 Span ID）关联起来，从而在分析性能数据时能够上下文地关联到具体的请求跟踪
	s := grpc.NewServer(
		//grpc.UnaryInterceptor：添加一个拦截器用于一元（unary）gRPC方法。
		//otelgrpc.UnaryServerInterceptor()：这是一个OpenTelemetry的拦截器，用于追踪gRPC请求。它会在每个一元gRPC请求处理时创建一个追踪跨度（span），并将其传播到下游服务，以实现分布式追踪。
		grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()), //添加OpenTelemetry的Unary拦截器：
		//grpc.ChainUnaryInterceptor：用于将多个一元拦截器链式组合在一起。
		//srvMetrics.UnaryServerInterceptor：这是Prometheus的拦截器，用于收集一元gRPC请求的度量指标。
		//grpcprom.WithExemplarFromContext(prom.ExtractContext)：配置拦截器从上下文中提取样本（exemplars），这些样本可以用于更详细的指标分析。
		//注意：样本数据（exemplars） 是Prometheus的一个特性，用于将特定的标识信息（如Trace ID、Span ID）附加到度量指标的样本中。这些样本数据在度量记录中添加了额外的上下文，使得你可以在查询和分析度量数据时，快速定位到相关的追踪信息或事件。
		grpc.ChainUnaryInterceptor(srvMetrics.UnaryServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))), //链式添加Prometheus的Unary拦截器
		//grpc.ChainStreamInterceptor：用于将多个流（stream）拦截器链式组合在一起。
		//srvMetrics.StreamServerInterceptor：这是Prometheus的拦截器，用于收集流式gRPC请求的度量指标。
		//grpcprom.WithExemplarFromContext(prom.ExtractContext)：同样配置拦截器从上下文中提取样本。
		//注意：样本数据（exemplars） 是Prometheus的一个特性，用于将特定的标识信息（如Trace ID、Span ID）附加到度量指标的样本中。这些样本数据在度量记录中添加了额外的上下文，使得你可以在查询和分析度量数据时，快速定位到相关的追踪信息或事件。
		grpc.ChainStreamInterceptor(srvMetrics.StreamServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))), //链式添加Prometheus的Stream拦截器
	)

	//lable:将服务名称和端口信息注册到Consul，以便其他服务能够发现并访问该服务
	if err := consul.RegisterConsul(config.FeedRpcServerName, config.FeedRpcServerPort); err != nil {
		log.Panicf("Rpc %s register consul happens error for: %v", config.FeedRpcServerName, err)
	}
	log.Infof("Rpc %s is running at %s now", config.FeedRpcServerName, config.FeedRpcServerPort)

	var srv FeedServiceImpl
	//RegisterFeedServiceServer：这是 gRPC 代码生成工具提供的一个函数。
	//它的作用是将 FeedService 的服务实现注册到一个 gRPC 服务器上。
	//这个函数是根据你的 .proto 文件（定义了服务及其方法）自动生成的。
	feed.RegisterFeedServiceServer(s, srv)
	//s：通常是一个 gRPC 服务器的实例，通常由 grpc.NewServer() 创建。这个服务器会处理传入的 RPC 调用
	//srv：这是服务的实际实现，应该实现了 FeedService 接口中定义的方法。

	grpc_health_v1.RegisterHealthServer(s, health.NewServer()) //将健康检查服务注册到一个gRPC 服务器上。这使得 gRPC 服务器能够响应健康检查请求
	defer CloseMQConn()
	if err := consul.RegisterConsul(config.FeedRpcServerName, config.FeedRpcServerPort); err != nil {
		log.Panicf("Rpc %s register consul happens error for: %v", config.FeedRpcServerName, err)
	}
	srv.New()                       //New() 函数用于创建一个新的服务实例。具体的实现细节取决于 srv 包的定义
	srvMetrics.InitializeMetrics(s) //InitializeMetrics(s) 函数用于初始化与服务 s 相关的指标。这可能包括设置监控指标、注册指标收集器等，以便能够监控和记录服务的性能和健康状况。

	//这段代码使用了一个名为 g 的组对象（通常是 errgroup.Group），用于并行执行多个函数，并在其中一个函数出错时进行清理操作。
	g := &run.Group{}
	g.Add(func() error { //这个函数启动 gRPC 服务器 s，并开始监听传入的连接
		return s.Serve(lis)
	}, func(err error) { //这个函数在第一个函数返回错误时执行，用于进行清理操作。
		s.GracefulStop()
		s.Stop()
		log.Errorf("Rpc %s listen happens error for: %v", config.FeedRpcServerName, err)
	})

	httpSrv := &http.Server{Addr: config.EnvCfg.PodIpAddr + config.Metrics}
	//创建一个新的 HTTP 服务器实例，监听地址为 config.EnvCfg.PodIpAddr + config.Metrics，
	//其中 config.EnvCfg.PodIpAddr 是服务器的 IP 地址，config.Metrics = 37099 是监听的端口或路径。
	g.Add(func() error {
		m := http.NewServeMux()
		m.Handle("/metrics", promhttp.HandlerFor( //注册一个处理器来处理 /metrics 路径的请求,promhttp.HandlerFor用于创建一个 Prometheus 指标处理器
			reg, // Prometheus 注册器，用于收集和暴露指标
			promhttp.HandlerOpts{
				EnableOpenMetrics: true,
			},
		))
		httpSrv.Handler = m
		log.Infof("Promethus now running")
		return httpSrv.ListenAndServe() //启动 HTTP 服务器，开始监听和服务请求
	}, func(error) {
		if err := httpSrv.Close(); err != nil {
			log.Errorf("Prometheus %s listen happens error for: %v", config.FeedRpcServerName, err)
		}
	})

	//向 errgroup.Group 中添加一个信号处理器，用于处理系统信号（如 SIGINT 和 SIGTERM），并在接收到这些信号时触发相关的清理操作
	g.Add(run.SignalHandler(context.Background(), syscall.SIGINT, syscall.SIGTERM))
	//run.SignalHandler:创建一个信号处理器
	//context.Background()：这是一个根上下文，通常用于传递给其他函数来派生新的上下文。这里用于信号处理器，确保它可以在需要时取消操作。
	//syscall.SIGINT, syscall.SIGTERM：这两个参数是系统信号。SIGINT 通常是在用户按下 Ctrl+C 时发送的信号，而 SIGTERM 通常用于请求程序正常终止。这两个信号用于通知程序应该停止运行并进行清理。
	//g.Add(...)：添加了一个信号处理器，用于处理 SIGINT 和 SIGTERM 信号，确保在接收到这些信号时能够优雅地关闭服务器并进行清理。

	//g.Run() 是一个阻塞调用，启动并运行 errgroup.Group 中添加的所有任务。
	//如果其中任何一个任务返回错误，g.Run() 会返回该错误，并且所有任务都会被取消
	if err := g.Run(); err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when runing http server")
		os.Exit(1)
	}
}
