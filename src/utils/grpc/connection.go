// Package grpc
// @Description: grpc的链接，consul服务发现，负载均衡策略，客户端拦截器实现trace跟踪，
package grpc

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/utils/logging"
	"fmt"
	_ "github.com/mbobakov/grpc-consul-resolver"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"time"
)

// Connect
// 利用 Consul 提供的服务发现机制，通过 gRPC 协议建立与指定服务的连接，并设置了保持连接活跃、负载均衡策略和拦截器等功能，
// 以支持分布式系统中的服务通信和调用。
//
//	@Description:
//
//	@param serviceName  要连接的服务名称
//	@return conn 返回一个 *grpc.ClientConn 类型的连接对象 conn，用于与目标服务通信
func Connect(serviceName string) (conn *grpc.ClientConn) {
	//step：设置了保持连接活跃的参数，包括发送频率、超时时间等。
	//配置客户端的保活（keepalive）参数。保活机制通常用于检测和维护长连接的状态，确保连接在没有数据传输时不会因超时而断开。
	kacp := keepalive.ClientParameters{
		//这个字段设置了客户端发送保活（ping）消息的时间间隔。在这个例子中，10 * time.Second 表示如果连接在 10 秒内没有任何活动（即没有数据发送或接收），客户端将发送一个 ping 消息。
		Time: 10 * time.Second, // send pings every 10 seconds if there is no activity
		//这个字段定义了客户端等待 ping 确认（ack）的最长时间。如果在这个时间之后没有收到确认，客户端将认为连接已经断开。在这个例子中，time.Second 表示客户端将等待 1 秒。
		Timeout: time.Second, // wait 1 second for ping ack before considering the connection dead
		//这个字段指示是否允许在没有活动流（stream）的情况下发送保活消息。在这个例子中，false 表示只有在存在活动连接时才会发送保活消息。如果设置为 true，则即使没有数据传输，也会按 Time 指定的间隔发送保活消息。
		PermitWithoutStream: false, // send pings even without active streams
	}

	//step：使用 grpc.Dial 函数建立与 gRPC 服务的连接
	conn, err := grpc.Dial(
		//note：连接地址通过 Consul 进行解析，使用 consul:// 协议指定 Consul 地址、服务名称和额外的参数 wait=15s。
		//focus：consul的用处在这里
		fmt.Sprintf("consul://%s/%s?wait=15s", config.EnvCfg.ConsulAddr, config.EnvCfg.ConsulAnonymityPrefix+serviceName),
		grpc.WithTransportCredentials(insecure.NewCredentials()),                //指定使用不安全的传输凭据
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy": "round_robin"}`), //lable:指定默认的服务配置，此处设置了负载均衡策略为轮询
		//，otelgrpc.UnaryClientInterceptor() 被传递给了 grpc.Dial，从而在每次 gRPC Unary 调用时自动应用 OpenTelemetry 的追踪和监控功能。
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()), //lable:注册了 gRPC 的客户端拦截器，用于进行请求拦截和跟踪
		grpc.WithKeepaliveParams(kacp),                               //lable:设置了上面定义的 keepalive 参数，以确保与服务之间的连接保持活跃
	)

	logging.Logger.Debugf("connect")

	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"service": config.EnvCfg.ConsulAnonymityPrefix + serviceName,
			"err":     err,
		}).Errorf("Cannot connect to %v service", config.EnvCfg.ConsulAnonymityPrefix+serviceName)
	}
	return
}
