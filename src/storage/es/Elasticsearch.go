package es

import (
	"GuGoTik/src/constant/config"
	"log"

	es "github.com/elastic/go-elasticsearch/v7"
)

var EsClient *es.Client

// init
//
//	@Description: ES初始化代码，配置和连接到 Elasticsearch，并创建一个名为 "Message" 的索引
func init() {
	cfg := es.Config{
		Addresses: []string{
			config.EnvCfg.ElasticsearchUrl, //Elasticsearch 的地址列表
		},
	}
	var err error
	EsClient, err = es.NewClient(cfg)
	if err != nil {
		log.Fatalf("elasticsearch.NewClient: %v", err)
	}

	//获取 Elasticsearch 信息
	_, err = EsClient.Info()
	if err != nil {
		log.Fatalf("Error getting response: %s", err)
	}

	//创建一个名为 "Message" 的索引
	_, err = EsClient.API.Indices.Create("Message")

	if err != nil {
		log.Fatalf("create index error: %s", err)
	}

}
