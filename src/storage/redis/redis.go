package redis

import (
	"GuGoTik/src/constant/config"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"strings"
)

var Client redis.UniversalClient

// init
//
//	@Description: 配置和初始化 Redis 客户端，并为其添加分布式追踪和指标收集功能
func init() {
	addrs := strings.Split(config.EnvCfg.RedisAddr, ";")
	Client = redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:      addrs,
		Password:   config.EnvCfg.RedisPassword,
		DB:         config.EnvCfg.RedisDB,
		MasterName: config.EnvCfg.RedisMaster,
	})

	//启用分布式追踪
	//redisotel 是一个用于 OpenTelemetry 的 Redis 集成库
	if err := redisotel.InstrumentTracing(Client); err != nil {
		panic(err)
	}

	//为 Redis 客户端启用指标收集功能，方便对 Redis 操作的性能进行监控。
	if err := redisotel.InstrumentMetrics(Client); err != nil {
		panic(err)
	}
}
