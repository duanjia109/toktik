package main

import (
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/storage/es"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/rabbitmq"
	"bytes"
	"context"
	"encoding/json"

	"github.com/elastic/go-elasticsearch/esapi"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
)

// esSaveMessage
//
//	@Description: 从"message_es"队列中消费消息，将从"message_es"队列中收到的消息插入到ES中的指定索引中
//	@param channel
func esSaveMessage(channel *amqp.Channel) {

	//从"message_es"队列中消费消息，返回一个管道
	msg, err := channel.Consume(strings.MessageES, "",
		false, false, false, false, nil,
	)
	//strings.MessageES 是要消费的消息队列的名称
	//consumer: 消费者名称。用来区分不同的消费者，可以为空字符串，RabbitMQ 会自动生成一个唯一的消费者名称
	//exclusive: 是否独占。如果设置为 true，这个消费者独占这个队列，其他消费者不能访问这个队列。设置为 false 允许多个消费者消费同一个队列
	//exclusive: 是否独占。如果设置为 true，这个消费者独占这个队列，其他消费者不能访问这个队列。设置为 false 允许多个消费者消费同一个队列。
	//
	//noLocal: 如果设置为 true，服务器不会将本连接发布的消息传递给这个消费者（仅适用于订阅模式）。一般情况下，这个选项使用较少，可以设置为 false。
	//
	//noWait: 如果设置为 true，队列会立刻返回而不等待服务器的响应。使用这种方式需要对消息队列有更深的理解，一般设置为 false。
	//args: 其他可选参数。通常情况下可以传 nil。

	//返回值：
	//msg: 返回一个接收通道 (<-chan amqp.Delivery)，从中可以读取队列中的消息。
	//err: 如果出错，会返回相应的错误信息
	failOnError(err, "Failed to Consume")

	var message models.Message
	//主要功能
	//从RabbitMQ消费消息：从RabbitMQ队列中读取消息。
	//解包消息：将消息体从JSON格式解包到Go结构体。
	//处理消息：将消息转换为Elasticsearch文档并插入Elasticsearch。
	//确认消息处理结果：根据处理结果确认或拒绝RabbitMQ消息。
	for body := range msg {
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), body.Headers)
		ctx, span := tracing.Tracer.Start(ctx, "MessageSendService")
		logger := logging.LogService("MessageSend").WithContext(ctx)

		//step：解包，尝试将消息体从JSON格式解包到message结构体
		if err := json.Unmarshal(body.Body, &message); err != nil {
			logger.WithFields(logrus.Fields{
				"from_id": message.FromUserId,
				"to_id":   message.ToUserId,
				"content": message.Content,
				"err":     err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)
			//note：如果解包失败，记录错误并拒绝该消息（Nack），要求RabbitMQ重新投递
			err = body.Nack(false, true)
			if err != nil {
				logger.WithFields(
					logrus.Fields{
						"from_id": message.FromUserId,
						"to_id":   message.ToUserId,
						"content": message.Content,
						"err":     err,
					},
				).Errorf("Error when nack the message")
				logging.SetSpanError(span, err)
			}
			//结束当前跨度并继续处理下一条消息
			span.End()
			continue
		}

		//step：解包成功，将消息转换为Elasticsearch文档格式（EsMessage）
		EsMessage := models.EsMessage{
			ToUserId:       message.ToUserId,
			FromUserId:     message.FromUserId,
			ConversationId: message.ConversationId,
			Content:        message.Content,
			CreateTime:     message.CreatedAt,
		}
		data, _ := json.Marshal(EsMessage)

		//step：使用 esapi库向 Elasticsearch 发送索引请求，将数据存储到 Elasticsearch 的指定索引中
		//创建 IndexRequest:
		//Index: "message"：指定索引名称为 "message"。在 Elasticsearch 中，索引相当于数据库中的表。
		//Refresh: "true"：表示在操作完成后立即刷新索引，使得新文档可以立刻被搜索到。
		//Body: bytes.NewReader(data)：表示文档的内容是从 data 变量读取的字节数据。
		req := esapi.IndexRequest{
			Index:   "message",
			Refresh: "true",
			Body:    bytes.NewReader(data),
		}

		//req.Do(ctx, es.EsClient)：执行索引请求。
		//ctx：上下文，用于控制请求的生命周期，可以用来设置超时或取消操作。
		//es.EsClient：Elasticsearch 客户端，用于与 Elasticsearch 服务器通信。
		//res：请求的响应，包含索引操作的结果。
		//err：如果请求过程中发生错误，则会在此变量中返回。
		res, err := req.Do(ctx, es.EsClient)

		//如果插入Elasticsearch失败，记录错误，结束当前跨度，并继续处理下一条消息。
		//手动确认：
		//如果消费者使用手动确认模式（即 autoAck 设置为 false），消息在没有被确认之前是处于未确认状态。
		//如果消费者在处理消息时崩溃或断开连接，RabbitMQ 会自动将未确认的消息重新放回队列，以确保这些消息不会丢失并能够被重新消费。
		if err != nil {
			logger.WithFields(logrus.Fields{
				"from_id": message.FromUserId,
				"to_id":   message.ToUserId,
				"content": message.Content,
				"err":     err,
			}).Errorf("Error when insert message to database.")
			logging.SetSpanError(span, err)

			span.End()
			continue // 如果处理失败，不确认消息，消息会被重新入队
		}
		//如果成功，关闭响应体（res.Body.Close()）。
		res.Body.Close()

		//确认消息已成功处理（Ack）
		err = body.Ack(false)

		//如果确认失败，记录错误
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when dealing with the message...3")
			logging.SetSpanError(span, err)
		}

	}
}
