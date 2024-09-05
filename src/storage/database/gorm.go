package database

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/utils/logging"
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"gorm.io/plugin/dbresolver"
	"gorm.io/plugin/opentelemetry/tracing"
	"strings"
	"time"
)

var Client *gorm.DB

// init
//
//	@Description: 初始化函数，用于配置和连接到 PostgreSQL 数据库
//
// focus：配置了数据库副本和读写分离
func init() {
	var err error

	//step:定义gorm框架的日志对象
	gormLogrus := logging.GetGormLogger()

	var cfg gorm.Config
	//step:初始化 GORM 配置，包括预编译语句、日志记录器和命名策略。
	if config.EnvCfg.PostgreSQLSchema == "" {
		cfg = gorm.Config{
			PrepareStmt: true,
			Logger:      gormLogrus,
			NamingStrategy: schema.NamingStrategy{
				TablePrefix: config.EnvCfg.PostgreSQLSchema + "." + config.EnvCfg.PostgreSQLPrefix,
			},
		}
	} else {
		cfg = gorm.Config{
			PrepareStmt: true,
			Logger:      gormLogrus,
			NamingStrategy: schema.NamingStrategy{
				TablePrefix: config.EnvCfg.PostgreSQLSchema + "." + config.EnvCfg.PostgreSQLPrefix,
			},
		}
	}

	//step:连接到 PostgreSQL 数据库。
	if Client, err = gorm.Open(
		postgres.Open(
			fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s",
				config.EnvCfg.PostgreSQLHost,     //rdb
				config.EnvCfg.PostgreSQLUser,     //gugotik
				config.EnvCfg.PostgreSQLPassword, //gugotik123
				config.EnvCfg.PostgreSQLDataBase, //gugodb
				config.EnvCfg.PostgreSQLPort)),   //5432
		&cfg,
	); err != nil {
		panic(err)
	}

	//step：根据配置选择是否启用数据库副本，并配置副本连接。
	//q：副本怎么使用的？主从读写？
	if config.EnvCfg.PostgreSQLReplicaState == "enable" {
		var replicas []gorm.Dialector
		//解析副本地址，创建副本连接配置。
		for _, addr := range strings.Split(config.EnvCfg.PostgreSQLReplicaAddress, ",") {
			pair := strings.Split(addr, ":")
			if len(pair) != 2 {
				continue
			}

			//为每个解析后的地址创建一个 gorm.Dialector 实例，并将其加入到副本列表 replicas 中。
			replicas = append(replicas, postgres.Open(
				fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s",
					pair[0],
					config.EnvCfg.PostgreSQLReplicaUsername,
					config.EnvCfg.PostgreSQLReplicaPassword,
					config.EnvCfg.PostgreSQLDataBase,
					pair[1])))
		}

		//q：使用 dbresolver 插件将副本注册到 GORM 客户端中，配置副本列表和负载均衡策略，使用随机策略（RandomPolicy）。
		err := Client.Use(dbresolver.Register(dbresolver.Config{
			Replicas: replicas,
			Policy:   dbresolver.RandomPolicy{},
		}))
		if err != nil {
			panic(err)
		}
	}

	//step：配置数据库连接池
	sqlDB, err := Client.DB()
	if err != nil {
		panic(err)
	}

	sqlDB.SetMaxIdleConns(100)
	sqlDB.SetMaxOpenConns(200)
	sqlDB.SetConnMaxLifetime(24 * time.Hour)
	sqlDB.SetConnMaxIdleTime(time.Hour)

	//step：加载并使用 GORM 插件（例如，追踪插件）
	if err := Client.Use(tracing.NewPlugin()); err != nil {
		panic(err)
	}
}
