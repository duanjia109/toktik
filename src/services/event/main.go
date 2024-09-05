// Package main//Packagemain
// @Description:负责推荐事件的相关处理
package main // Package main
// @Description:

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/gorse"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/rabbitmq"
	"context"
	"encoding/json"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"strconv"
	"sync"
	"time"
)

func exitOnError(err error) {
	if err != nil {
		panic(err)
	}
}

// main
//
//	@Description: 初始化mq连接，交换机，队列等，并开启consumer协程来消费event_queue队列中的数据
func main() {
	//step：初始化连接
	conn, err := amqp.Dial(rabbitmq.BuildMQConnAddr())
	exitOnError(err)

	defer func(conn *amqp.Connection) {
		err := conn.Close()
		exitOnError(err)
	}(conn)

	tp, err := tracing.SetTraceProvider(config.Event)
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

	//step: 连接 - 管道
	ch, err := conn.Channel()
	exitOnError(err)

	defer func(ch *amqp.Channel) {
		err := ch.Close()
		exitOnError(err)
	}(ch)

	//主要功能：声明一个交换机，用于接收消息并根据路由键将消息路由到绑定的队列。
	//ExchangeDeclare 的主要作用是声明一个交换机（Exchange），用于路由消息。如果不声明交换机，消息将无法通过该交换机进行路由，进而无法到达绑定的队列。
	err = ch.ExchangeDeclare(
		strings.EventExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)

	//声明一个用于存储消息的队列。
	q, err := ch.QueueDeclare(
		"event_queue",
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)

	err = ch.Qos(1, 0, false)
	exitOnError(err)

	//绑定队列：QueueBind 方法将队列与交换机绑定，并指定路由键。
	//绑定操作是将队列和交换机连接起来，使得交换机上的消息可以根据路由键分发到相应的队列中
	//消息路由：当交换机接收到一个消息时，如果消息的路由键匹配 "video.#"，则消息将被路由到 q.Name 指定的队列。
	err = ch.QueueBind( //lable:将队列名"event_queue"与交换机strings.EventExchange的"video.#"关键字绑定
		q.Name,                //队列名
		"video.#",             //路由键key
		strings.EventExchange, //交换机名称
		false,
		nil)

	exitOnError(err)
	go Consume(ch, q.Name) //lable:从"event_queue"队列中消费信息
	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

var gorseClient *gorse.GorseClient

func init() {
	//q：推荐api的服务器地址？
	gorseClient = gorse.NewGorseClient(config.EnvCfg.GorseAddr, config.EnvCfg.GorseApiKey)
}

// Consume
//
//	@Description: 使用ch从queueName中消费信息，负责推荐事件的相关处理
//	@param ch
//	@param queueName
func Consume(ch *amqp.Channel, queueName string) {
	//ch.Consume 方法用于订阅队列并开始接收从队列中发送来的消息
	//msg：一个接收消息的通道（<-chan amqp.Delivery），通过读取这个通道可以接收到队列中的消息。
	msg, err := ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		panic(err)
	}

	for d := range msg {
		//解包 Otel Context
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), d.Headers)
		ctx, span := tracing.Tracer.Start(ctx, "EventSystem")
		logger := logging.LogService("EventSystem.Recommend").WithContext(ctx)
		logging.SetSpanWithHostname(span)
		var raw models.RecommendEvent
		//note：将消息的主体（d.Body）反序列化为一个Go结构（raw）
		if err := json.Unmarshal(d.Body, &raw); err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)
			continue
		}

		//step:处理特定类型的消息（raw.Type 为 1），并将相关的反馈信息插入到 gorse 系统中
		switch raw.Type {
		case 1: //已读消息的反馈
			//q：这个已读是推荐过就算已读吗？有没有检测观看时间？
			var types string
			switch raw.Source {
			case config.FeedRpcServerName:
				types = "read"
			}
			var feedbacks []gorse.Feedback //note：用的是feedback类型
			for _, id := range raw.VideoId {
				feedbacks = append(feedbacks, gorse.Feedback{
					FeedbackType: types,
					UserId:       strconv.Itoa(int(raw.ActorId)),
					ItemId:       strconv.Itoa(int(id)),
					Timestamp:    time.Now().UTC().Format(time.RFC3339),
				})
			}

			//step：对读过的视频插入反馈
			if _, err := gorseClient.InsertFeedback(ctx, feedbacks); err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when insert the feedback")
				logging.SetSpanError(span, err) //q:如果插入失败不需要重试吗？
			}
			logger.WithFields(logrus.Fields{
				"ids": raw.VideoId,
			}).Infof("Event dealt with type 1")
			span.End()
			err = d.Ack(false) //确认消息已被成功处理 (d.Ack(false))。
			//q:如果在消息确认处理成功之前崩溃了，怎么处理？
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when ack")
				logging.SetSpanError(span, err)
			}
		case 2: //评论或点赞行为的反馈
			var types string
			switch raw.Source {
			//step：根据source不同设置types不同
			case config.CommentRpcServerName:
				types = "comment"
			case config.FavoriteRpcServerName:
				types = "favorite"
			}
			var feedbacks []gorse.Feedback
			for _, id := range raw.VideoId {
				feedbacks = append(feedbacks, gorse.Feedback{ //Note:feedback类型
					FeedbackType: types,
					UserId:       strconv.Itoa(int(raw.ActorId)),
					ItemId:       strconv.Itoa(int(id)),
					Timestamp:    time.Now().UTC().Format(time.RFC3339),
				})
			}

			//step：插入反馈
			if _, err := gorseClient.InsertFeedback(ctx, feedbacks); err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when insert the feedback")
				logging.SetSpanError(span, err)
			}
			logger.WithFields(logrus.Fields{
				"ids": raw.VideoId,
			}).Infof("Event dealt with type 2")
			span.End()
			err = d.Ack(false)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when ack")
				logging.SetSpanError(span, err)
			}
		case 3: //q：这是什么？发布新视频
			var items []gorse.Item
			for _, id := range raw.VideoId {
				items = append(items, gorse.Item{ //note：item类型
					ItemId:     strconv.Itoa(int(id)),
					IsHidden:   false,
					Labels:     raw.Tag,
					Categories: raw.Category,
					Timestamp:  time.Now().UTC().Format(time.RFC3339),
					Comment:    raw.Title,
				})
			}

			//step： 推荐系统插入？
			if _, err := gorseClient.InsertItems(ctx, items); err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when insert the items")
				logging.SetSpanError(span, err)
			}
			logger.WithFields(logrus.Fields{
				"ids":     raw.VideoId,
				"tag":     raw.Tag,
				"comment": raw.Title,
			}).Infof("Event dealt with type 3")
			span.End()
			err = d.Ack(false)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when ack")
				logging.SetSpanError(span, err)
			}
		}
	}
}
