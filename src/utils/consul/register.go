// Package consul
// @Description: consul的客户端来做服务注册
package consul

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/utils/logging"
	"fmt"
	"github.com/google/uuid"
	capi "github.com/hashicorp/consul/api"
	log "github.com/sirupsen/logrus"
	"strconv"
)

var consulClient *capi.Client

// 创建配置信息并初始化consulClient
func init() {
	cfg := capi.DefaultConfig()
	cfg.Address = config.EnvCfg.ConsulAddr         //地址
	if c, err := capi.NewClient(cfg); err == nil { //创建客户端
		consulClient = c
		return
	} else {
		logging.Logger.Panicf("Connect Consul happens error: %v", err)
	}
}

// RegisterConsul
// 向Consul注册服务实例
//
//	@Description: 具体来说，该函数将服务的名称和端口号注册到 Consul，并配置了健康检查
//	@param name  服务名称
//	@param port  服务端口
//	@return error
func RegisterConsul(name string, port string) error {
	//step：解析端口号
	parsedPort, err := strconv.Atoi(port[1:]) // port start with ':' which like ':37001'
	logging.Logger.WithFields(log.Fields{
		"name": name,
		"port": parsedPort,
	}).Infof("Services Register Consul")
	//q：ConsulAnonymityPrefix是什么？
	name = config.EnvCfg.ConsulAnonymityPrefix + name //"paraparty." + name

	if err != nil {
		return err
	}
	//step：创建一个服务注册的结构体实例,用于描述要注册的服务
	reg := &capi.AgentServiceRegistration{
		ID:      fmt.Sprintf("%s-%s", name, uuid.New().String()[:5]), //服务的唯一标识，由服务名和一个生成的短 UUID 组成
		Name:    name,                                                //服务名称
		Address: config.EnvCfg.PodIpAddr,                             //服务地址  q：为什么是固定的？
		Port:    parsedPort,                                          //服务端口号
		Check: &capi.AgentServiceCheck{ //用于健康检查的结构体，描述服务健康检查的参数
			Interval:                       "5s",
			Timeout:                        "5s",
			GRPC:                           fmt.Sprintf("%s:%d", config.EnvCfg.PodIpAddr, parsedPort),
			GRPCUseTLS:                     false,
			DeregisterCriticalServiceAfter: "30s",
		},
	}

	//step：调用 Consul 客户端的 ServiceRegister 方法将服务注册到 Consul。
	if err := consulClient.Agent().ServiceRegister(reg); err != nil {
		return err
	}
	return nil
}
