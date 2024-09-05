// Package main
// @Description: 主要从各个队列中拿取数据，并进行不同的消费行为，包括：
// 用gpt处理对话消息，在数据库中保存Message和Audit数据，还有向ES插入Message数据
package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/chat"
	"GuGoTik/src/storage/database"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/rabbitmq"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	url2 "net/url"
	"sync"

	"fmt"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sashabaranov/go-openai"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

var chatClient chat.ChatServiceClient
var conn *amqp.Connection
var channel *amqp.Channel

// 记录日志的
func failOnError(err error, msg string) {
	//打日志
	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf(msg)
	}
}

var delayTime = int32(2 * 60 * 1000) //2 minutes
var maxRetries = int32(3)

var openaiClient *openai.Client

// 初始化chatgpt客户端和相关配置
func init() {
	cfg := openai.DefaultConfig(config.EnvCfg.ChatGPTAPIKEYS)
	url, err := url2.Parse(config.EnvCfg.ChatGptProxy)
	if err != nil {
		panic(err)
	}
	cfg.HTTPClient = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(url),
		},
	}
	openaiClient = openai.NewClientWithConfig(cfg)
}

func CloseMQConn() {
	if err := conn.Close(); err != nil {
		panic(err)
	}

	if err := channel.Close(); err != nil {
		panic(err)
	}
}

// main
//
//	@Description: 定义了mq转发消息的规则，并且起了几个协程去消费消息
func main() {
	chatRpcConn := grpc2.Connect(config.MessageRpcServerName)
	chatClient = chat.NewChatServiceClient(chatRpcConn)

	var err error
	//amqp.Dial：连接到 RabbitMQ 服务器。amqp 是一个流行的 Go 客户端库，用于与 RabbitMQ 进行交互。
	//conn：保存 RabbitMQ 连接对象。
	//err：保存可能发生的错误。
	//step：建立rabbitmq连接对象
	conn, err = amqp.Dial(rabbitmq.BuildMQConnAddr())
	failOnError(err, "Failed to connect to RabbitMQ")

	tp, err := tracing.SetTraceProvider(config.MsgConsumer)
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

	//step：从已经建立的 RabbitMQ 连接 conn 中打开一个 RabbitMQ 逻辑通道，用于发送和接收消息。
	channel, err = conn.Channel()
	if err != nil {
		failOnError(err, "Failed to open a channel")
	}

	//step：声明了一个延迟消息交换机（"message_exchange"）
	//q:什么是延迟交换器？
	//通过 channel.ExchangeDeclare 声明了一个延迟消息交换机（x-delayed-message），
	//该交换机支持延迟消息传递功能。通过设置 "x-delayed-type": "topic"，可以使用主题路由的方式进行消息传递。
	err = channel.ExchangeDeclare(
		strings.MessageExchange, //这是交换机的名称，来自 strings 包中的常量或变量。
		"x-delayed-message",     //交换机的类型，这里是一个特殊的类型 x-delayed-message，用于实现延迟消息的功能。这个类型需要插件支持。
		true, false, false, false,
		amqp.Table{ //交换机的其他参数，这里使用一个表来设置额外的参数。"x-delayed-type": "topic" 表示延迟消息的类型为 topic，这意味着消息会按照主题路由
			"x-delayed-type": "topic",
		},
	)
	failOnError(err, "Failed to get exchange")

	//step: 声明一个直接交换机（"audit_exchange"）
	//direct 交换机是 RabbitMQ 中的一种标准交换机类型。它根据消息的路由键（routing key）将消息路由到绑定了相同路由键的队列。
	err = channel.ExchangeDeclare(
		strings.AuditExchange, //交换机名称
		"direct",              //交换机种类
		true, false, false, false,
		nil,
	)
	failOnError(err, fmt.Sprintf("Failed to get %s exchange", strings.AuditExchange))

	//step:声明4个队列
	//QueueDeclare 是 RabbitMQ 客户端库中用于声明（创建）队列的函数。该函数创建一个新的队列，
	//如果同名队列已经存在，则返回现有队列而不会修改其属性。
	_, err = channel.QueueDeclare(
		strings.MessageCommon, //队列名称
		true, false, false, false,
		nil,
	)
	failOnError(err, "Failed to define queue")

	_, err = channel.QueueDeclare(
		strings.MessageGPT,
		true, false, false, false,
		nil,
	)
	failOnError(err, "Failed to define queue")

	_, err = channel.QueueDeclare(
		strings.MessageES,
		true, false, false, false,
		nil,
	)
	failOnError(err, "Failed to define queue")

	_, err = channel.QueueDeclare(
		strings.AuditPicker,
		true, false, false, false,
		nil,
	)
	failOnError(err, fmt.Sprintf("Failed to define %s queue", strings.AuditPicker))

	//step: 将4个队列绑定到交换机上，以便交换机根据指定的路由键（routing key）将消息路由到队列中
	//将名为 strings.MessageCommon 的队列绑定到名为 strings.MessageExchange 的交换器上，使用路由键模式 "message.#"
	err = channel.QueueBind(
		strings.MessageCommon,   //队列名称
		"message.#",             //这是一个路由键模式。路由键用于将消息路由到一个或多个队列。这里的模式 "message.#" 表示匹配以 "message." 开头的任何字符串。# 是一个特殊字符，它匹配一个或多个单词（由点分隔的部分）
		strings.MessageExchange, //交换器名称。队列将被绑定到这个交换器。
		false,                   //绑定操作应该立即执行，而不是异步执行
		nil,
	)
	failOnError(err, "Failed to bind queue to exchange")

	//q：es是干嘛的，方便日志索引？
	err = channel.QueueBind( //用于es消费
		strings.MessageES,
		"message.#",
		strings.MessageExchange,
		false,
		nil,
	)
	failOnError(err, "Failed to bind queue to exchange")

	err = channel.QueueBind(
		strings.MessageGPT,
		strings.MessageGptActionEvent,
		strings.MessageExchange,
		false,
		nil,
	)
	failOnError(err, "Failed to bind queue to exchange")

	err = channel.QueueBind( //用于audit消费
		strings.AuditPicker,
		strings.AuditPublishEvent,
		strings.AuditExchange,
		false,
		nil,
	)
	failOnError(err, fmt.Sprintf("Failed to bind %s queue to %s exchange", strings.AuditPicker, strings.AuditExchange))

	//step：起了多个协程来消费消息
	go saveMessage(channel) //lable：把Message存到数据库中，消费message_common队列
	logger := logging.LogService("MessageSend")
	logger.Infof(strings.MessageActionEvent + " is running now")

	go chatWithGPT(channel) //lable：gpt回复用户消息，消费message_gpt队列
	logger = logging.LogService("MessageGPTSend")
	logger.Infof(strings.MessageGptActionEvent + " is running now")

	go saveAuditAction(channel) //lable：在数据库插入Action表，消费"audit_picker"队列
	logger = logging.LogService("AuditPublish")
	logger.Infof(strings.AuditPublishEvent + " is running now")

	go esSaveMessage(channel) //lable：插入es，消费"message_es"队列
	logger = logging.LogService("esSaveMessage")
	logger.Infof(strings.VideoPicker + " is running now")

	defer CloseMQConn()

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

// saveMessage
//
//	@Description: 从"message_common"队列中消费数据并存到数据库的Message表中
//	@param channel
func saveMessage(channel *amqp.Channel) {
	msg, err := channel.Consume(
		strings.MessageCommon, //队列名称
		"",                    //消费者
		false, false, false, false,
		nil,
	)
	failOnError(err, "Failed to Consume")
	var message models.Message
	for body := range msg {
		//从 AMQP 消息头中提取上下文信息，并使用 OpenTelemetry 进行分布式跟踪。
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), body.Headers)

		ctx, span := tracing.Tracer.Start(ctx, "MessageSendService")
		logger := logging.LogService("MessageSend").WithContext(ctx)

		// Check if it is a re-publish message
		//q：最多重试一次？
		//note：理解这里是在控制重试的阈值，重试次数超过指定阈值（例如 1 次），则跳过该消息的进一步处理，避免重复处理导致的无限循环或资源浪费
		retry, ok := body.Headers["x-retry"].(int32)
		if ok || retry >= 1 { //x-retry 字段存在且值大于或等于 1
			err := body.Ack(false)
			if err != nil { //确认消息失败
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when dealing with the message...")
				logging.SetSpanError(span, err)
			}
			span.End() //结束当前的追踪跨度
			continue   //继续处理下一个消息
		}

		//step: 解析消息体
		//q：处理消息队列消息时，如果消息体的 JSON 解析失败，如何处理错误并进行必要的日志记录和消息队列操作
		if err := json.Unmarshal(body.Body, &message); err != nil {
			logger.WithFields(logrus.Fields{
				"from_id": message.FromUserId,
				"to_id":   message.ToUserId,
				"content": message.Content,
				"err":     err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)
			err = body.Nack(false, true) //否认消息，表示消息处理失败，希望消息重新排队
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
			span.End()
			continue
		}

		pmessage := models.Message{
			ToUserId:       message.ToUserId,
			FromUserId:     message.FromUserId,
			ConversationId: message.ConversationId,
			Content:        message.Content,
		}
		logger.WithFields(logrus.Fields{
			"message": pmessage,
		}).Debugf("Receive message event")

		//q：可能会重新插入数据 开启事务 晚点改
		//lable: 在注释中提到的 "开启事务" 意味着在写入数据库操作之前，需要通过数据库操作框架提供的方法来启动一个数据库事务。事务是数据库操作的一种机制，它可以确保一系列操作要么全部成功执行，要么全部失败回滚，从而保持数据的一致性和完整性。
		//step： 写入数据库
		result := database.Client.WithContext(ctx).Create(&pmessage)

		if result.Error != nil {
			logger.WithFields(logrus.Fields{
				"from_id": message.FromUserId,
				"to_id":   message.ToUserId,
				"content": message.Content,
				"err":     result.Error,
			}).Errorf("Error when insert message to database.")
			logging.SetSpanError(span, err)
			//false 表示消息不会被重新排队到队列的开头，而是放回到队列的尾部，即保持原有的顺序。
			//true 表示消息应该被重新排队，这通常意味着消息将被重新处理，以尝试解决处理期间出现的问题。
			err = body.Nack(false, true) //在消息处理失败时否认消息，并要求消息队列系统将消息重新放回队列，以便稍后重新处理
			if err != nil {
				logger.WithFields(
					logrus.Fields{
						"from_id": message.FromUserId,
						"to_id":   message.ToUserId,
						"content": message.Content,
						"err":     err,
					}).Errorf("Error when nack the message")
				logging.SetSpanError(span, err)
			}
			span.End()
			continue
		}
		//multiple=false（单条确认）：表示仅确认当前这条消息。也就是说，只有这条消息会被标记为已处理。
		//multiple=true（批量确认）：表示确认所有小于等于当前交付标签（delivery tag）的消息。
		//这意味着不仅当前这条消息会被确认，所有在此之前接收到的未确认消息也会被一起确认。
		err = body.Ack(false) //Ack 是指消息确认操作，表示消费者已经成功处理了消息

		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when dealing with the message...")
			logging.SetSpanError(span, err)
		}
		span.End()
	}
}

// chatWithGPT
// gpt处理用户发来的消息并调用ChatAction回复
//
//	@Description: 从"message_gpt"队列消费消息（这个队列是用户发给gpt的消息）：
//
// 1.从队列接收消息并解码，出错设置重新发布机制
// 2.请求gpt生成对话回复
// 3.把回复发送到消息队列
//
//	@param channel
func chatWithGPT(channel *amqp.Channel) {
	gptmsg, err := channel.Consume(
		strings.MessageGPT,
		"",
		false, false, false, false,
		nil,
	)
	if err != nil {
		failOnError(err, "Failed to Consume")
	}
	var message models.Message

	for body := range gptmsg {
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), body.Headers)
		ctx, span := tracing.Tracer.Start(ctx, "MessageGPTSendService")
		logger := logging.LogService("MessageGPTSend").WithContext(ctx)

		if err := json.Unmarshal(body.Body, &message); err != nil {
			logger.WithFields(logrus.Fields{
				"from_id": message.FromUserId,
				"to_id":   message.ToUserId,
				"content": message.Content,
				"err":     err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)

			//note：解码失败 重新发布消息
			//q: 为什么要手动实现？如何自动实现？
			errorHandler(channel, body, false, logger, &span)
			span.End()
			continue
		}

		//解码成功，记录日志
		logger.WithFields(logrus.Fields{
			"content": message.Content,
		}).Debugf("Receive ChatGPT message event")

		//定义了一个与 OpenAI 的 GPT-3.5 Turbo 模型交互的请求
		req := openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo, //模型
			Messages: []openai.ChatCompletionMessage{ //消息列表
				{
					Role:    openai.ChatMessageRoleUser,
					Content: message.Content,
				},
			},
		}

		//step：Create a completion for the chat message
		//用于与 OpenAI 的 GPT-3.5 或 GPT-4 等模型进行对话生成。它实现了一个对话完成功能，
		//接受一系列对话历史作为输入，并返回模型生成的对话回复。
		resp, err := openaiClient.CreateChatCompletion(ctx, req)

		if err != nil {
			logger.WithFields(logrus.Fields{
				"Err":     err,
				"from_id": message.FromUserId,
				"context": message.Content,
			}).Errorf("Failed to get reply from ChatGPT")

			logging.SetSpanError(span, err)
			//重试
			errorHandler(channel, body, true, logger, &span)
			span.End()
			continue
		}

		text := resp.Choices[0].Message.Content
		logger.WithFields(logrus.Fields{
			"reply": text,
		}).Infof("Successfully get the reply for ChatGPT")

		//lable：rpc调用  chat.ChatService ChatAction
		//step：把gpt的回复消息作为请求参数，调用ChatAction发送出去
		_, err = chatClient.ChatAction(ctx, &chat.ActionRequest{
			ActorId:    message.ToUserId,
			UserId:     message.FromUserId,
			ActionType: 1,
			Content:    text,
		})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Replying to user happens error")
			errorHandler(channel, body, true, logger, &span) //失败重试
			continue
		}
		logger.Infof("Successfully send the reply to user")

		err = body.Ack(false) //成功回复ack

		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when dealing with the message...")
			logging.SetSpanError(span, err)
		}
		span.End()
	}
}

// saveAuditAction
// 存数据到Action表
//
//	@Description: 从"audit_picker"队列拿出一条消息，插入一条数据库记录到Action表
//	@param channel
func saveAuditAction(channel *amqp.Channel) {
	msg, err := channel.Consume(
		strings.AuditPicker,
		"",
		false, false, false, false,
		nil,
	)
	failOnError(err, "Failed to Consume")

	var action models.Action
	for body := range msg {
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), body.Headers)

		ctx, span := tracing.Tracer.Start(ctx, "AuditPublishService")
		logger := logging.LogService("AuditPublish").WithContext(ctx)

		//step：消费一条数据
		if err := json.Unmarshal(body.Body, &action); err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)
			//拒绝接受消息或通知消息队列系统某个消息的处理失败
			//第一个参数表示仅拒绝当前的一条消息；第二个参数表示消息会被重新放回到队列中，以便稍后再次处理
			err = body.Nack(false, true)
			if err != nil {
				logger.WithFields(
					logrus.Fields{
						"err":         err,
						"Type":        action.Type,
						"SubName":     action.SubName,
						"ServiceName": action.ServiceName,
					},
				).Errorf("Error when nack the message")
				logging.SetSpanError(span, err)
			}
			span.End()
			continue //处理下一条消息
		}

		pAction := models.Action{
			Type:         action.Type,
			Name:         action.Name,
			SubName:      action.SubName,
			ServiceName:  action.ServiceName,
			Attached:     action.Attached,
			ActorId:      action.ActorId,
			VideoId:      action.VideoId,
			AffectAction: action.AffectAction,
			AffectedData: action.AffectedData,
			EventId:      action.EventId,
			TraceId:      action.TraceId,
			SpanId:       action.SpanId,
		}
		logger.WithFields(logrus.Fields{
			"action": pAction,
		}).Debugf("Recevie action event")

		//step：插入一条数据库Action数据
		result := database.Client.WithContext(ctx).Create(&pAction)
		if result.Error != nil { //q: 插入错误，没有重试机制？
			logger.WithFields(
				logrus.Fields{
					"err":         err,
					"Type":        action.Type,
					"SubName":     action.SubName,
					"ServiceName": action.ServiceName,
				},
			).Errorf("Error when nack the message")
			logging.SetSpanError(span, err)
			err = body.Nack(false, true) //note: 这里重试了
			if err != nil {
				logger.WithFields(
					logrus.Fields{
						"err":         err,
						"Type":        action.Type,
						"SubName":     action.SubName,
						"ServiceName": action.ServiceName,
					},
				).Errorf("Error when nack the message")
				logging.SetSpanError(span, err)
			}
			span.End()
			continue
		}
		//note：插入成功确认消息
		err = body.Ack(false) //false，则只确认当前消息。

		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when dealing with the action...")
			logging.SetSpanError(span, err)
		}
		span.End()
	}
}

// errorHandler
// 根据重试次数设置消息的重新发布，发布到交换机"message_exchange"，路由键"message.gpt"
//
//	@Description:首先根据requeue判断是否重新发布，然后根据重试次数判断，超过重试次数就不再重发，直接取消消息；
//
// 如果没有超过重试次数，更新重试次数等元数据之后再进行重发
// q: mq本身难道没有实现重发的机制吗？这样手动重发会不会引发并发问题？
//
//	@param channel  逻辑通道
//	@param d  要重发的消息
//	@param requeue  false代表拒绝消息，但不会重新将消息放回队列，即消息会被丢弃或进入死信队列；
//	true代表会重新发布消息
//	@param logger
//	@param span
func errorHandler(channel *amqp.Channel, d amqp.Delivery, requeue bool, logger *logrus.Entry, span *trace.Span) {
	if !requeue { //step：直接拒绝
		//拒绝消息，但不会重新将消息放回队列，即消息会被丢弃或进入死信队列
		err := d.Nack(false, false) //第一个false参数：仅拒绝当前这条消息；第二个false参数：消息不会重新入队，消息将被丢弃或发送到死信队列（如果配置了死信队列）
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when nacking the message event...")
			logging.SetSpanError(*span, err)
		}
	} else { //step：Re-publish the message
		curRetry, ok := d.Headers["x-retry"].(int32)
		if !ok {
			curRetry = 0
		}
		//如果当前重试次数 curRetry 达到设定的最大重试次数 maxRetries，则记录错误信息并将消息确认（d.Ack(false)）。
		if curRetry >= maxRetries { //lable：超过最大重试次数，不再重新发布
			logger.WithFields(logrus.Fields{
				"body": d.Body,
			}).Errorf("Maximum retries reached for message.")
			logging.SetSpanError(*span, errors.New("maximum retries reached for message"))
			err := d.Ack(false) //q:什么意思？
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when dealing with the message event...")
			}
		} else { //lable：没超过，设置重发
			//增加当前重试次数 curRetry，并更新消息头部的重试次数和延迟时间
			curRetry++
			headers := d.Headers
			headers["x-delay"] = delayTime
			headers["x-retry"] = curRetry

			err := d.Ack(false) //q：对原始消息进行确认（d.Ack(false)），确保消息不会重新进入队列
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when dealing with the message event...")
			}

			logger.Debugf("Retrying %d times", curRetry)

			//使用 channel.PublishWithContext 方法重新发布消息到交换机"message_exchange"，路由键"message.gpt"
			err = channel.PublishWithContext(
				context.Background(),
				strings.MessageExchange,       //交换机
				strings.MessageGptActionEvent, //路由键
				false,
				false,
				amqp.Publishing{
					DeliveryMode: amqp.Persistent,
					ContentType:  "text/plain",
					Body:         d.Body,
					Headers:      headers,
				},
			)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when re-publishing the message event to queue...")
				logging.SetSpanError(*span, err)
			}
		}
	}
}
