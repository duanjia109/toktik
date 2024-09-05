// Package main
// @Description: 主要完成聊天消息的发送功能
package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/chat"
	"GuGoTik/src/rpc/feed"
	"GuGoTik/src/rpc/recommend"
	"GuGoTik/src/rpc/relation"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/redis"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/ptr"
	"GuGoTik/src/utils/rabbitmq"
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/robfig/cron/v3"

	"time"

	"github.com/go-redis/redis_rate/v10"
	"gorm.io/gorm"

	"github.com/sirupsen/logrus"
)

var userClient user.UserServiceClient
var recommendClient recommend.RecommendServiceClient
var relationClient relation.RelationServiceClient
var feedClient feed.FeedServiceClient
var chatClient chat.ChatServiceClient

type MessageServiceImpl struct {
	chat.ChatServiceServer
}

// 连接
var conn *amqp.Connection
var channel *amqp.Channel

// 输出
func failOnError(err error, msg string) {
	//打日志
	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf(msg)
	}
}

// New
//
//	@Description:
//	1.初始化conn，channel，声明了MessageExchange交换机，3个队列，并且绑定交换机和3个队列的路由逻辑
//	2.创建微服务客户端并连接
//	3.运行定时任务sendMagicMessage
//	@receiver c
func (c MessageServiceImpl) New() {
	var err error

	conn, err = amqp.Dial(rabbitmq.BuildMQConnAddr())
	failOnError(err, "Failed to connect to RabbitMQ")

	channel, err = conn.Channel()
	failOnError(err, "Failed to open a channel")

	//step : 声明一个 AMQP 交换机，具体来说是一个支持延迟消息的交换机。
	//它使用了 RabbitMQ 的插件 "x-delayed-message" 来实现消息延迟投递功能
	err = channel.ExchangeDeclare(
		strings.MessageExchange,
		"x-delayed-message",
		true, false, false, false,
		amqp.Table{
			"x-delayed-type": "topic", //表示延迟消息使用的实际交换机类型为 "topic"
		},
	)
	failOnError(err, "Failed to get exchange")
	//step: 声明三个队列
	_, err = channel.QueueDeclare(
		strings.MessageCommon,
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

	//step：把3个队列绑定在交换机上
	err = channel.QueueBind( //q：message是生产者，也需要定义这么多吗？
		strings.MessageCommon,
		"message.#",
		strings.MessageExchange,
		false,
		nil,
	)
	failOnError(err, "Failed to bind queue to exchange")

	err = channel.QueueBind(
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

	//step：通过 gRPC 连接不同的服务，并创建相应的客户端
	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	userClient = user.NewUserServiceClient(userRpcConn)

	recommendRpcConn := grpc2.Connect(config.RecommendRpcServiceName)
	recommendClient = recommend.NewRecommendServiceClient(recommendRpcConn)

	relationRpcConn := grpc2.Connect(config.RelationRpcServerName)
	relationClient = relation.NewRelationServiceClient(relationRpcConn)

	feedRpcConn := grpc2.Connect(config.FeedRpcServerName)
	feedClient = feed.NewFeedServiceClient(feedRpcConn)

	chatRpcConn := grpc2.Connect(config.MessageRpcServerName)
	chatClient = chat.NewChatServiceClient(chatRpcConn)

	//还创建了一个定时任务的运行器（cronRunner），并启用了秒级精度的任务调度
	cronRunner := cron.New(cron.WithSeconds())

	//step：创建定时任务sendMagicMessage
	//_, err = cronRunner.AddFunc("0 0 18 * * *", sendMagicMessage) // 每天18:00执行
	_, err = cronRunner.AddFunc("@every 5m", sendMagicMessage) // 每5分钟执行一次（测试用）

	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Cannot start SendMagicMessage cron job")
	}

	cronRunner.Start() //这行代码启动 cronRunner，开始调度并执行已添加的定时任务。

}

// CloseMQConn
//
//	@Description: 关闭rabbitmq的channel和conn
func CloseMQConn() {
	if err := channel.Close(); err != nil {
		failOnError(err, "close channel error")
	}
	if err := conn.Close(); err != nil {
		failOnError(err, "close conn error")
	}
}

//发送消息

var chatActionLimitKeyPrefix = config.EnvCfg.RedisPrefix + "chat_freq_limit"

const chatActionMaxQPS = 3

func chatActionLimitKey(userId uint32) string {
	return fmt.Sprintf("%s-%d", chatActionLimitKeyPrefix, userId)
}

// ChatAction
// 主要完成发送消息的功能，request.ActorId ——> request.UserId，通过调用addMessage函数来做
//
//	@Description:
//	1.对request.ActorId用户速率限制
//	2.确认用户是否存在
//	3.调用addMessage函数：把消息发送到消息队列上
//	@receiver c
//	@param ctx
//	@param request   request.ActorId 向 request.UserId 发送 request.Content消息
//	@return res
//	@return err
func (c MessageServiceImpl) ChatAction(ctx context.Context, request *chat.ActionRequest) (res *chat.ActionResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ChatActionService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("ChatService.ActionMessage").WithContext(ctx)

	logger.WithFields(logrus.Fields{
		"ActorId":      request.ActorId,
		"user_id":      request.UserId,
		"action_type":  request.ActionType,
		"content_text": request.Content,
	}).Debugf("Process start")

	//step: Rate limiting
	//使用 redis_rate 库进行速率限制，以限制用户的聊天操作频率
	//通过 Redis 来实现每秒最多允许的请求数量，从而避免用户在短时间内进行过多操作
	limiter := redis_rate.NewLimiter(redis.Client)    //创建了一个新的限流器（limiter），并将 Redis 客户端传递给它
	limiterKey := chatActionLimitKey(request.ActorId) //定义了限流键,基于用户 ID生成，确保每个用户有独立的限流计数器

	//limiterRes：表示限流检查的结果。
	//- Allowed：bool，指示是否允许当前请求。如果为 true，表示请求未超过限流限制，可以继续执行；如果为 false，表示请求超过了限流限制，应该进行相应的处理。
	//- Wait：time.Duration，如果 Allowed 是 false，表示需要等待多长时间（Wait）才能再次进行请求。
	//err：error，在执行过程中发生的错误。如果为 nil，表示限流检查成功。
	//q: 这里限制的是每个用户每秒的访问频率是3,随着用户越来越多，应该如何动态调整？
	limiterRes, err := limiter.Allow(ctx, limiterKey, redis_rate.PerSecond(chatActionMaxQPS)) //调用 limiter.Allow 方法检查当前操作是否超过限流限制,是否允许当前操作
	if err != nil {
		logger.WithFields(logrus.Fields{
			"ActorId":      request.ActorId,
			"user_id":      request.UserId,
			"action_type":  request.ActionType,
			"content_text": request.Content,
		}).Errorf("ChatAction limiter error")

		res = &chat.ActionResponse{
			StatusCode: strings.UnableToAddMessageErrorCode,
			StatusMsg:  strings.UnableToAddMessageError,
		}
		return
	}
	if limiterRes.Allowed == 0 { //限流未通过
		logger.WithFields(logrus.Fields{
			"ActorId":      request.ActorId,
			"user_id":      request.UserId,
			"action_type":  request.ActionType,
			"content_text": request.Content,
		}).Errorf("Chat action query too frequently by user %d", request.ActorId)

		res = &chat.ActionResponse{
			StatusCode: strings.ChatActionLimitedCode,
			StatusMsg:  strings.ChatActionLimitedError,
		}
		return
	}

	//获取request.UserId是否存在
	userResponse, err := userClient.GetUserExistInformation(ctx, &user.UserExistRequest{
		UserId: request.UserId,
	})

	if err != nil || userResponse.StatusCode != strings.ServiceOKCode { //q：加入出错了就返回就结束了吗？没有自动重试什么的？
		logger.WithFields(logrus.Fields{
			"err":          err,
			"ActorId":      request.ActorId,
			"user_id":      request.UserId,
			"action_type":  request.ActionType,
			"content_text": request.Content,
		}).Errorf("User service error")
		logging.SetSpanError(span, err)

		return &chat.ActionResponse{
			StatusCode: strings.UnableToAddMessageErrorCode,
			StatusMsg:  strings.UnableToAddMessageError,
		}, err
	}

	if !userResponse.Existed {
		return &chat.ActionResponse{
			StatusCode: strings.UserDoNotExistedCode,
			StatusMsg:  strings.UserNotExisted,
		}, nil
	}
	//step: 调用addMessage函数发送消息：request.ActorId ——> request.UserId
	res, err = addMessage(ctx, request.ActorId, request.UserId, request.Content)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":          err,
			"user_id":      request.UserId,
			"action_type":  request.ActionType,
			"content_text": request.Content,
		}).Errorf("database insert error")
		logging.SetSpanError(span, err)
		return res, err
	}

	logger.WithFields(logrus.Fields{
		"response": res,
	}).Debugf("Process done.")

	return res, err
}

// Chat Chat(context.Context, *ChatRequest) (*ChatResponse, error)
//
//	@Description:
//	@receiver c
//	@param ctx
//	@param request
//	@return resp
//	@return err
func (c MessageServiceImpl) Chat(ctx context.Context, request *chat.ChatRequest) (resp *chat.ChatResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ChatService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("ChatService.chat").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"user_id":      request.UserId,
		"ActorId":      request.ActorId,
		"pre_msg_time": request.PreMsgTime,
	}).Debugf("Process start")

	userResponse, err := userClient.GetUserExistInformation(ctx, &user.UserExistRequest{
		UserId: request.UserId,
	})

	if err != nil || userResponse.StatusCode != strings.ServiceOKCode {
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
			"user_id": request.UserId,
		}).Errorf("User service error")
		logging.SetSpanError(span, err)

		resp = &chat.ChatResponse{
			StatusCode: strings.UnableToQueryMessageErrorCode,
			StatusMsg:  strings.UnableToQueryMessageError,
		}
		return
	}

	if !userResponse.Existed {
		return &chat.ChatResponse{
			StatusCode: strings.UserDoNotExistedCode,
			StatusMsg:  strings.UserNotExisted,
		}, nil
	}

	//确定from 和to
	toUserId := request.UserId
	fromUserId := request.ActorId

	conversationId := fmt.Sprintf("%d_%d", toUserId, fromUserId) //组合成一个conversationId: toUserId_fromUserId

	if toUserId > fromUserId {
		conversationId = fmt.Sprintf("%d_%d", fromUserId, toUserId)
	}
	//这个地方应该取出多少条消息？
	//TO DO 看怎么需要改一下

	//note: 从数据库中找pMessageList ，根据conversationId
	var pMessageList []models.Message
	var result *gorm.DB
	if request.PreMsgTime == 0 {
		result = database.Client.WithContext(ctx).
			Where("conversation_id=?", conversationId).
			Order("created_at").
			Find(&pMessageList)
	} else {
		result = database.Client.WithContext(ctx).
			Where("conversation_id=?", conversationId).
			Where("created_at > ?", time.UnixMilli(int64(request.PreMsgTime)).Add(100*time.Millisecond)).
			Order("created_at").
			Find(&pMessageList)
	}

	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err":          result.Error,
			"user_id":      request.UserId,
			"ActorId":      request.ActorId,
			"pre_msg_time": request.PreMsgTime,
		}).Errorf("ChatServiceImpl list chat failed to response when listing message, database err")
		logging.SetSpanError(span, err)

		resp = &chat.ChatResponse{
			StatusCode: strings.UnableToQueryMessageErrorCode,
			StatusMsg:  strings.UnableToQueryMessageError,
		}
		return
	}

	//把pMessageList里的数据包装成rMessageList
	rMessageList := make([]*chat.Message, 0, len(pMessageList))
	if request.PreMsgTime == 0 {
		for _, pMessage := range pMessageList {

			rMessageList = append(rMessageList, &chat.Message{
				Id:         pMessage.ID,
				Content:    pMessage.Content,
				CreateTime: uint64(pMessage.CreatedAt.UnixMilli()),
				FromUserId: ptr.Ptr(pMessage.FromUserId),
				ToUserId:   ptr.Ptr(pMessage.ToUserId),
			})
		}
	} else {
		for _, pMessage := range pMessageList {
			if pMessage.ToUserId == request.ActorId {
				rMessageList = append(rMessageList, &chat.Message{
					Id:         pMessage.ID,
					Content:    pMessage.Content,
					CreateTime: uint64(pMessage.CreatedAt.UnixMilli()),
					FromUserId: ptr.Ptr(pMessage.FromUserId),
					ToUserId:   ptr.Ptr(pMessage.ToUserId),
				})
			}
		}
	}

	resp = &chat.ChatResponse{
		StatusCode:  strings.ServiceOKCode,
		StatusMsg:   strings.ServiceOK,
		MessageList: rMessageList,
	}

	logger.WithFields(logrus.Fields{
		"response": resp,
	}).Debugf("Process done.")

	return
}

// addMessage
// 根据发送方向的不同将消息发送到不同的路由键上
//
//	@Description:把消息发送到mq上
//	@param ctx
//	@param fromUserId 发送者
//	@param toUserId  接收者
//	@param Context  发送消息内容
//	@return resp
//	@return err
func addMessage(ctx context.Context, fromUserId uint32, toUserId uint32, Context string) (resp *chat.ActionResponse, err error) {
	conversationId := fmt.Sprintf("%d_%d", toUserId, fromUserId)

	//两个人通信，只有一个conversationId，小的在前，大的在后
	if toUserId > fromUserId {
		conversationId = fmt.Sprintf("%d_%d", fromUserId, toUserId)
	}

	//step：构建消息
	message := models.Message{
		ToUserId:       toUserId,
		FromUserId:     fromUserId,
		Content:        Context,
		ConversationId: conversationId,
	}
	message.Model = gorm.Model{ //给消息设置创建时间为当前时间
		CreatedAt: time.Now(),
	}

	body, err := json.Marshal(message)
	if err != nil {
		resp = &chat.ActionResponse{
			StatusCode: strings.UnableToAddMessageErrorCode,
			StatusMsg:  strings.UnableToAddMessageError,
		}
		return
	}

	//step: 发送到消息队列
	headers := rabbitmq.InjectAMQPHeaders(ctx)
	if message.ToUserId == config.EnvCfg.MagicUserId { //如果是发给MagicUserId的
		logging.Logger.WithFields(logrus.Fields{
			"routing": strings.MessageGptActionEvent,
			"message": message,
		}).Debugf("Publishing message to %s", strings.MessageGptActionEvent)
		err = channel.PublishWithContext( //发布到交换机"message_exchange"的"message.gpt"路由键
			ctx,
			strings.MessageExchange,
			strings.MessageGptActionEvent, //note：发布到"message.gpt"路由键
			false,
			false,
			amqp.Publishing{
				DeliveryMode: amqp.Persistent,
				ContentType:  "text/plain",
				Body:         body,
				Headers:      headers,
			})
		if err != nil {
			logging.Logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Failed to publish message to %s", strings.MessageGptActionEvent)
		}

	} else { //否则是MagicUserId发给用户的
		logging.Logger.WithFields(logrus.Fields{
			"routing": strings.MessageActionEvent,
			"message": message,
		}).Debugf("Publishing message to %s", strings.MessageActionEvent)
		err = channel.PublishWithContext(
			ctx,
			strings.MessageExchange,
			strings.MessageActionEvent, //note：发布到"message.common"路由键
			false,
			false,
			amqp.Publishing{
				DeliveryMode: amqp.Persistent,
				ContentType:  "text/plain",
				Body:         body,
				Headers:      headers,
			})
		if err != nil {
			logging.Logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Failed to publish message to %s", strings.MessageActionEvent)
		}
	}

	// result := database.Client.WithContext(ctx).Create(&message)

	if err != nil {
		logging.Logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when publishing the message to mq")
		resp = &chat.ActionResponse{
			StatusCode: strings.UnableToAddMessageErrorCode,
			StatusMsg:  strings.UnableToAddMessageError,
		}
		return
	}

	resp = &chat.ActionResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
	}
	return

}

// sendMagicMessage
//
//	@Description: 获取MagicUserId所有的朋友，对每一位朋友调用recommendClient.GetRecommendInformation获取推荐视频id，
//
// 再调用chatClient.ChatAction发送视频推荐聊天信息
func sendMagicMessage() {
	ctx, span := tracing.Tracer.Start(context.Background(), "SendMagicMessageService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("ChatService.SendMessageService").WithContext(ctx)

	logger.Debugf("Start ChatService.SendMessageService at %s", time.Now())

	//step：Get all friends of magic user
	friendsResponse, err := relationClient.GetFriendList(ctx, &relation.FriendListRequest{
		ActorId: config.EnvCfg.MagicUserId,
		UserId:  config.EnvCfg.MagicUserId,
	})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"ActorId": config.EnvCfg.MagicUserId,
			"Err":     err,
		}).Errorf("Cannot get friend list of magic user")
		logging.SetSpanError(span, err)
		return
	}

	//step：Send magic message to every friends
	friends := friendsResponse.UserList
	videoMap := make(map[uint32]*feed.Video)
	for _, friend := range friends {
		// Get recommend video id
		recommendResponse, err := recommendClient.GetRecommendInformation(ctx, &recommend.RecommendRequest{
			UserId: friend.Id, //被推荐人的id
			Offset: 0,
			Number: 1, //推送一条？
		})

		if err != nil || len(recommendResponse.VideoList) < 1 {
			logger.WithFields(logrus.Fields{
				"UserId": friend.Id,
				"Err":    err,
			}).Errorf("Cannot get recommend video of user")
			logging.SetSpanError(span, err)
			continue
		}

		//step：Get videoinfo by video id
		videoId := recommendResponse.VideoList[0]
		video, ok := videoMap[videoId] //videoMap中是否存在videoId的详细信息
		if !ok {                       //不存在访问详细视频信息
			videoQueryResponse, err := feedClient.QueryVideos(ctx, &feed.QueryVideosRequest{
				ActorId:  config.EnvCfg.MagicUserId,
				VideoIds: []uint32{videoId},
			})
			if err != nil || len(videoQueryResponse.VideoList) < 1 {
				logger.WithFields(logrus.Fields{
					"UserId":  friend.Id,
					"VideoId": videoId,
					"Err":     err,
				}).Errorf("Cannot get video info of %d", videoId)
				logging.SetSpanError(span, err)
				continue
			}
			video = videoQueryResponse.VideoList[0]
			videoMap[videoId] = video
		}

		//step：Chat to every friend
		content := fmt.Sprintf("今日视频推荐：%s；\n视频链接：%s", video.Title, video.PlayUrl)
		//lable：rpc调用 ChatService ChatAction
		// 实现MagicUserId向friend.Id发送content消息
		_, err = chatClient.ChatAction(ctx, &chat.ActionRequest{
			ActorId:    config.EnvCfg.MagicUserId,
			UserId:     friend.Id,
			ActionType: 1,
			Content:    content,
		})

		if err != nil {
			logger.WithFields(logrus.Fields{
				"UserId":  friend.Id,
				"VideoId": videoId,
				"Content": content,
				"Err":     err,
			}).Errorf("Cannot send magic message to user %d", friend.Id)
			logging.SetSpanError(span, err)
			continue
		}

		logger.WithFields(logrus.Fields{
			"UserId":  friend.Id,
			"VideoId": videoId,
			"Content": content,
		}).Infof("Successfully send the magic message")
	}
}
