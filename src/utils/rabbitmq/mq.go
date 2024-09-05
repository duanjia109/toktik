package rabbitmq

import (
	"GuGoTik/src/constant/config"
	"fmt"
)

// rabbitmq.BuildMQConnAddr()：构建 RabbitMQ 连接地址
func BuildMQConnAddr() string {
	return fmt.Sprintf("amqp://%s:%s@%s:%s/%s", config.EnvCfg.RabbitMQUsername, config.EnvCfg.RabbitMQPassword,
		config.EnvCfg.RabbitMQAddr, config.EnvCfg.RabbitMQPort, config.EnvCfg.RabbitMQVhostPrefix)
}
