package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/profiling"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/chat"
	"GuGoTik/src/utils/consul"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/prom"
	"context"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"net/http"
	"os"
	"syscall"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

func main() {
	//step：设置tracing.SetTraceProvider
	tp, err := tracing.SetTraceProvider(config.MessageRpcServerName)

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

	//step：Configure Pyroscope
	profiling.InitPyroscope("GuGoTik.ChatService")

	//step：监听自己服务端口
	log := logging.LogService(config.MessageRpcServerName)
	lis, err := net.Listen("tcp", config.EnvCfg.PodIpAddr+config.MessageRpcServerPort)

	if err != nil {
		log.Panicf("Rpc %s listen happens error: %v", config.MessageRpcServerName, err)
	}

	//step：设置普罗米修斯配置？
	srvMetrics := grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}),
		),
	)

	reg := prom.Client
	reg.MustRegister(srvMetrics)

	//step：设置defer关闭rabbitmq
	defer CloseMQConn()

	//step：启动一个grpc的服务端？
	s := grpc.NewServer(
		grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.ChainUnaryInterceptor(srvMetrics.UnaryServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))),
		grpc.ChainStreamInterceptor(srvMetrics.StreamServerInterceptor(grpcprom.WithExemplarFromContext(prom.ExtractContext))),
	)

	//step:注册consul
	if err := consul.RegisterConsul(config.MessageRpcServerName, config.MessageRpcServerPort); err != nil {
		log.Panicf("Rpc %s register consul happens error for: %v", config.MessageRpcServerName, err)
	}
	log.Infof("Rpc %s is running at %s now", config.MessageRpcServerName, config.MessageRpcServerPort)

	var srv MessageServiceImpl
	chat.RegisterChatServiceServer(s, srv)
	grpc_health_v1.RegisterHealthServer(s, health.NewServer())
	defer CloseMQConn()

	//step：一些初始化逻辑
	srv.New()
	srvMetrics.InitializeMetrics(s)

	//step：用run.Group{}组织多个函数的运行
	g := &run.Group{}
	g.Add(func() error { //note：运行本服务的服务
		return s.Serve(lis)
	}, func(err error) {
		s.GracefulStop()
		s.Stop()
		log.Errorf("Rpc %s listen happens error for: %v", config.MessageRpcServerName, err)
	})

	httpSrv := &http.Server{Addr: config.EnvCfg.PodIpAddr + config.Metrics}
	g.Add(func() error { //note：运行普罗米修斯的采集接口
		m := http.NewServeMux()
		m.Handle("/metrics", promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{
				EnableOpenMetrics: true,
			},
		))
		httpSrv.Handler = m
		log.Infof("Promethus now running")
		return httpSrv.ListenAndServe()
	}, func(error) {
		if err := httpSrv.Close(); err != nil {
			log.Errorf("Prometheus %s listen happens error for: %v", config.MessageRpcServerName, err)
		}
	})

	g.Add(run.SignalHandler(context.Background(), syscall.SIGINT, syscall.SIGTERM)) //note：信号的异常处理工作

	if err := g.Run(); err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when runing http server")
		os.Exit(1)
	}
}
