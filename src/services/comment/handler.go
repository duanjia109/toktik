package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/comment"
	"GuGoTik/src/rpc/feed"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/cached"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/redis"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/rabbitmq"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis_rate/v10"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"strconv"
	"sync"
	"time"
)

var userClient user.UserServiceClient
var feedClient feed.FeedServiceClient

var actionCommentLimitKeyPrefix = config.EnvCfg.RedisPrefix + "comment_freq_limit"

const actionCommentMaxQPS = 3 // Maximum ActionComment query amount of an actor per second

var rateCommentLimitKey = config.EnvCfg.RedisPrefix + "rate_comment_freq_limit"

const rateCommentMaxQPM = 3 // Maximum RateComment query amount

func exitOnError(err error) {
	if err != nil {
		panic(err)
	}
}

// Return redis key to record the amount of ActionComment query of an actor, e.g., comment_freq_limit-1-1669524458
func actionCommentLimitKey(userId uint32) string {
	return fmt.Sprintf("%s-%d", actionCommentLimitKeyPrefix, userId)
}

type CommentServiceImpl struct {
	comment.CommentServiceServer
}

var conn *amqp.Connection

var channel *amqp.Channel

func (c CommentServiceImpl) New() {
	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	userClient = user.NewUserServiceClient(userRpcConn)

	feedRpcConn := grpc2.Connect(config.FeedRpcServerName)
	feedClient = feed.NewFeedServiceClient(feedRpcConn)

	var err error

	conn, err = amqp.Dial(rabbitmq.BuildMQConnAddr())
	exitOnError(err)

	channel, err = conn.Channel()
	exitOnError(err)

	err = channel.ExchangeDeclare(
		strings.EventExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)
}

func CloseMQConn() {
	if err := conn.Close(); err != nil {
		panic(err)
	}

	if err := channel.Close(); err != nil {
		panic(err)
	}
}

// 将event数据发送到消息队列的"video.comment.action"
func produceComment(ctx context.Context, event models.RecommendEvent) {
	ctx, span := tracing.Tracer.Start(ctx, "CommentPublisher")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("CommentService.CommentPublisher").WithContext(ctx)
	data, err := json.Marshal(event)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when marshal the event model")
		logging.SetSpanError(span, err)
		return
	}

	headers := rabbitmq.InjectAMQPHeaders(ctx)

	//step：消息队列发布
	err = channel.PublishWithContext(ctx,
		strings.EventExchange,
		strings.VideoCommentEvent, //routingKey 是用于将消息路由到特定队列的关键字段。它是发布者发布消息时指定的一个标识符，用来告诉消息代理（如 RabbitMQ、Kafka 等）应该将消息发送到哪些接收者队列
		false,
		false,
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        data,
			Headers:     headers,
		})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when publishing the event model")
		logging.SetSpanError(span, err)
		return
	}
}

// ActionComment implements the CommentServiceImpl interface.
//
// ActionComment
//
//	@Description: 根据请求的 ActionType做出添加/删除评论操作，更新数据库和缓存等
//	@receiver c
//	@param ctx
//	@param request  用户请求
//	@return resp
//	@return err
func (c CommentServiceImpl) ActionComment(ctx context.Context, request *comment.ActionCommentRequest) (resp *comment.ActionCommentResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ActionCommentService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("CommentService.ActionComment").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"user_id":      request.ActorId,
		"video_id":     request.VideoId,
		"action_type":  request.ActionType,
		"comment_text": request.GetCommentText(),
		"comment_id":   request.GetCommentId(),
	}).Debugf("Process start")

	var pCommentText string
	var pCommentID uint32

	//step： 获取参数信息
	switch request.ActionType {
	case comment.ActionCommentType_ACTION_COMMENT_TYPE_ADD:
		pCommentText = request.GetCommentText() //note： 类型断言
	case comment.ActionCommentType_ACTION_COMMENT_TYPE_DELETE:
		pCommentID = request.GetCommentId()
	case comment.ActionCommentType_ACTION_COMMENT_TYPE_UNSPECIFIED:
		fallthrough
	default:
		logger.Warnf("Invalid action type")
		resp = &comment.ActionCommentResponse{
			StatusCode: strings.ActionCommentTypeInvalidCode,
			StatusMsg:  strings.ActionCommentTypeInvalid,
		}
		return
	}

	// step： Rate limiting
	limiter := redis_rate.NewLimiter(redis.Client)
	limiterKey := actionCommentLimitKey(request.ActorId)
	//q：限制每个人发送评论的频率？
	limiterRes, err := limiter.Allow(ctx, limiterKey, redis_rate.PerSecond(actionCommentMaxQPS))
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
		}).Errorf("ActionComment limiter error")
		logging.SetSpanError(span, err)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToCreateCommentErrorCode,
			StatusMsg:  strings.UnableToCreateCommentError,
		}

		return
	}
	if limiterRes.Allowed == 0 {
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
		}).Infof("Action comment query too frequently by user %d", request.ActorId)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.ActionCommentLimitedCode,
			StatusMsg:  strings.ActionCommentLimited,
		}

		return
	}

	//step：边界条件、异常参数校验
	//Check if video exists
	videoExistResp, err := feedClient.QueryVideoExisted(ctx, &feed.VideoExistRequest{
		VideoId: request.VideoId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Query video existence happens error")
		logging.SetSpanError(span, err)
		resp = &comment.ActionCommentResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
		}
		return
	}

	if !videoExistResp.Existed {
		logger.WithFields(logrus.Fields{
			"VideoId": request.VideoId,
		}).Errorf("Video ID does not exist")
		logging.SetSpanError(span, err)
		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToQueryVideoErrorCode,
			StatusMsg:  strings.UnableToQueryVideoError,
		}
		return
	}

	//step：拿到用户信息
	//lable: rpc调用 UserService GetUserInfo
	userResponse, err := userClient.GetUserInfo(ctx, &user.UserRequest{
		UserId:  request.ActorId,
		ActorId: request.ActorId,
	})

	if err != nil || userResponse.StatusCode != strings.ServiceOKCode {
		if userResponse.StatusCode == strings.UserNotExistedCode {
			resp = &comment.ActionCommentResponse{
				StatusCode: strings.UserDoNotExistedCode,
				StatusMsg:  strings.UserNotExisted,
			}
			return
		}
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
		}).Errorf("User service error")
		logging.SetSpanError(span, err)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToQueryUserErrorCode,
			StatusMsg:  strings.UnableToQueryUserError,
		}
		return
	}

	//用户信息
	pUser := userResponse.User

	//step：调用添加/删除评论接口
	switch request.ActionType {
	//根据参数ActionType不同
	case comment.ActionCommentType_ACTION_COMMENT_TYPE_ADD: //添加评论
		resp, err = addComment(ctx, logger, span, pUser, request.VideoId, pCommentText)
	case comment.ActionCommentType_ACTION_COMMENT_TYPE_DELETE: //删除评论
		resp, err = deleteComment(ctx, logger, span, pUser, request.VideoId, pCommentID)
	}

	if err != nil {
		return
	}

	//step：删除评论数量的缓存
	countCommentKey := fmt.Sprintf("CommentCount-%d", request.VideoId)
	cached.TagDelete(ctx, countCommentKey)

	logger.WithFields(logrus.Fields{
		"response": resp,
	}).Debugf("Process done.")

	return
}

// ListComment implements the CommentServiceImpl interface.
//
// ListComment
//
//	@Description:根据请求中的videoid找出所有的评论
//	@receiver c
//	@param ctx
//	@param request
//	@return resp
//	@return err
func (c CommentServiceImpl) ListComment(ctx context.Context, request *comment.ListCommentRequest) (resp *comment.ListCommentResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ListCommentService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("CommentService.ListComment").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"user_id":  request.ActorId,
		"video_id": request.VideoId,
	}).Debugf("Process start")

	// Check if video exists
	videoExistResp, err := feedClient.QueryVideoExisted(ctx, &feed.VideoExistRequest{
		VideoId: request.VideoId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Query video existence happens error")
		logging.SetSpanError(span, err)
		resp = &comment.ListCommentResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
		}
		return
	}

	if !videoExistResp.Existed {
		logger.WithFields(logrus.Fields{
			"VideoId": request.VideoId,
		}).Errorf("Video ID does not exist")
		logging.SetSpanError(span, err)
		resp = &comment.ListCommentResponse{
			StatusCode: strings.UnableToQueryVideoErrorCode,
			StatusMsg:  strings.UnableToQueryVideoError,
		}
		return
	}

	//step：从数据库中查找评论
	var pCommentList []models.Comment
	result := database.Client.WithContext(ctx).
		Where("video_id = ?", request.VideoId).
		Where("rate <= 3").
		//Where("moderation_flagged = false").
		Order("created_at desc").
		Find(&pCommentList)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err": result.Error,
		}).Errorf("CommentService list comment failed to response when listing comments")
		logging.SetSpanError(span, err)

		resp = &comment.ListCommentResponse{
			StatusCode: strings.UnableToQueryCommentErrorCode,
			StatusMsg:  strings.UnableToQueryCommentError,
		}
		return
	}

	// Put the magic comment to the first front
	reindexCommentList(&pCommentList)

	// Get user info of each comment
	//step: 准备userMap：userid - user.User
	rCommentList := make([]*comment.Comment, 0, result.RowsAffected)
	userMap := make(map[uint32]*user.User)
	for _, pComment := range pCommentList {
		userMap[pComment.UserId] = &user.User{} //userid - User{}
	}
	getUserInfoError := false

	wg := sync.WaitGroup{}
	wg.Add(len(userMap))
	for userId := range userMap {
		go func(userId uint32) {
			defer wg.Done()
			userResponse, getUserErr := userClient.GetUserInfo(ctx, &user.UserRequest{
				UserId:  userId,
				ActorId: request.ActorId,
			})
			if err != nil || userResponse.StatusCode != strings.ServiceOKCode {
				logger.WithFields(logrus.Fields{
					"err":     getUserErr,
					"user_id": userId,
				}).Errorf("Unable to get user info")
				logging.SetSpanError(span, getUserErr)
				getUserInfoError = true
				err = getUserErr
			}
			userMap[userId] = userResponse.User
		}(userId)
	}
	wg.Wait()

	if getUserInfoError {
		resp = &comment.ListCommentResponse{
			StatusCode: strings.UnableToQueryUserErrorCode,
			StatusMsg:  strings.UnableToQueryUserError,
		}
		return
	}

	// Create rCommentList
	for _, pComment := range pCommentList {
		curUser := userMap[pComment.UserId]

		//note： rCommentList只关心部分4个参数
		rCommentList = append(rCommentList, &comment.Comment{
			Id:         pComment.ID,
			User:       curUser, //发布评论的人
			Content:    pComment.Content,
			CreateDate: pComment.CreatedAt.Format("01-02"),
		})
	}

	//step： 返回结果
	resp = &comment.ListCommentResponse{
		StatusCode:  strings.ServiceOKCode,
		StatusMsg:   strings.ServiceOK,
		CommentList: rCommentList,
	}

	logger.WithFields(logrus.Fields{
		"response": resp,
	}).Debugf("Process done.")

	return
}

// CountComment implements the CommentServiceImpl interface.
func (c CommentServiceImpl) CountComment(ctx context.Context, request *comment.CountCommentRequest) (resp *comment.CountCommentResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "CountCommentService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("CommentService.CountComment").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"user_id":  request.ActorId,
		"video_id": request.VideoId,
	}).Debugf("Process start")

	countStringKey := fmt.Sprintf("CommentCount-%d", request.VideoId)
	countString, err := cached.GetWithFunc(ctx, countStringKey,
		func(ctx context.Context, key string) (string, error) {
			rCount, err := count(ctx, request.VideoId)

			return strconv.FormatInt(rCount, 10), err
		})

	if err != nil {
		cached.TagDelete(ctx, "CommentCount")
		logger.WithFields(logrus.Fields{
			"err":      err,
			"video_id": request.VideoId,
		}).Errorf("Unable to get comment count")
		logging.SetSpanError(span, err)

		resp = &comment.CountCommentResponse{
			StatusCode: strings.UnableToQueryCommentErrorCode,
			StatusMsg:  strings.UnableToQueryCommentError,
		}
		return
	}
	rCount, _ := strconv.ParseUint(countString, 10, 64)

	resp = &comment.CountCommentResponse{
		StatusCode:   strings.ServiceOKCode,
		StatusMsg:    strings.ServiceOK,
		CommentCount: uint32(rCount),
	}
	logger.WithFields(logrus.Fields{
		"response": resp,
	}).Debugf("Process done.")
	return
}

// addComment
// 添加评论，在gpt打分后插入到到数据库中的comment表中，然后把这条评论信息封装发送到消息队列中供推荐系统插入
//
//	@Description:
//	@param ctx
//	@param logger
//	@param span
//	@param pUser  评论者
//	@param pVideoID  被评论的视频id
//	@param pCommentText  评论文本
//	@return resp
//	@return err
func addComment(ctx context.Context, logger *logrus.Entry, span trace.Span, pUser *user.User, pVideoID uint32, pCommentText string) (resp *comment.ActionCommentResponse, err error) {
	rComment := models.Comment{
		VideoId: pVideoID,
		UserId:  pUser.Id,
		Content: pCommentText,
	}

	//step：新建一条评论记录插入到数据库
	result := database.Client.WithContext(ctx).Create(&rComment)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err":        result.Error,
			"comment_id": rComment.ID,
			"video_id":   pVideoID,
		}).Errorf("CommentService add comment action failed to response when adding comment")
		logging.SetSpanError(span, result.Error)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToCreateCommentErrorCode,
			StatusMsg:  strings.UnableToCreateCommentError,
		}
		return
	}

	// Rate comment
	//step：评论内容打分并更新到数据库中
	go rateComment(logger, span, pCommentText, rComment.ID)

	//step：发送到"video.comment.action"消息队列用于推荐算法
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		produceComment(ctx, models.RecommendEvent{
			ActorId: pUser.Id,
			VideoId: []uint32{pVideoID},
			Type:    2,
			Source:  config.CommentRpcServerName,
		})
	}()
	wg.Wait()

	resp = &comment.ActionCommentResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Comment: &comment.Comment{
			Id:         rComment.ID,
			User:       pUser,
			Content:    rComment.Content,
			CreateDate: rComment.CreatedAt.Format("01-02"),
		},
	}
	return
}

// deleteComment
// 删除一条评论
//
//	@Description:经过用户校验之后直接在数据库里删除就好
//	@param ctx
//	@param logger
//	@param span
//	@param pUser  谁要删除
//	@param pVideoID 评论对应的视频id
//	@param commentID  评论记录在表中的主键id
//	@return resp
//	@return err
func deleteComment(ctx context.Context, logger *logrus.Entry, span trace.Span, pUser *user.User, pVideoID uint32, commentID uint32) (resp *comment.ActionCommentResponse, err error) {
	rComment := models.Comment{}
	result := database.Client.WithContext(ctx).
		Where("video_id = ? AND id = ?", pVideoID, commentID).
		First(&rComment)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err":        result.Error,
			"video_id":   pVideoID,
			"comment_id": commentID,
		}).Errorf("Failed to get the comment")
		logging.SetSpanError(span, result.Error)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToQueryCommentErrorCode,
			StatusMsg:  strings.UnableToQueryCommentError,
		}
		return
	}

	//确认只有主人才能删除
	if rComment.UserId != pUser.Id {
		logger.Errorf("Comment creator and deletor not match")
		resp = &comment.ActionCommentResponse{
			StatusCode: strings.ActorIDNotMatchErrorCode,
			StatusMsg:  strings.ActorIDNotMatchError,
		}
		return
	}

	result = database.Client.WithContext(ctx).Delete(&models.Comment{}, commentID)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err": result.Error,
		}).Errorf("Failed to delete comment")
		logging.SetSpanError(span, result.Error)

		resp = &comment.ActionCommentResponse{
			StatusCode: strings.UnableToDeleteCommentErrorCode,
			StatusMsg:  strings.UnableToDeleteCommentError,
		}
		return
	}
	resp = &comment.ActionCommentResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Comment:    nil,
	}
	return
}

// rateComment
// 通过gpt对评论打分，并更新到数据库中
//
//	@Description:通过速率限制，调用 RateCommentByGPT 函数来执行对评论内容评分，并在数据库中更新评分等信息
//	@param logger
//	@param span
//	@param commentContent  要评分的评论值
//	@param commentID  该条评论的主键
func rateComment(logger *logrus.Entry, span trace.Span, commentContent string, commentID uint32) {
	//moderationCommentRes := ModerationCommentByGPT(commentContent, logger, span)
	//
	//rComment := models.Comment{
	//	ID:                        commentID,
	//	ModerationFlagged:         moderationCommentRes.Flagged,
	//	ModerationHate:            moderationCommentRes.Categories.Hate,
	//	ModerationHateThreatening: moderationCommentRes.Categories.HateThreatening,
	//	ModerationSelfHarm:        moderationCommentRes.Categories.SelfHarm,
	//	ModerationSexual:          moderationCommentRes.Categories.Sexual,
	//	ModerationSexualMinors:    moderationCommentRes.Categories.SexualMinors,
	//	ModerationViolence:        moderationCommentRes.Categories.Violence,
	//	ModerationViolenceGraphic: moderationCommentRes.Categories.ViolenceGraphic,
	//}

	rate := uint32(0)
	reason := ""

	limiter := redis_rate.NewLimiter(redis.Client)
	limiterKey := rateCommentLimitKey
	for {
		//step：限制对某个功能（可能是对评论进行评分）的调用频率
		limiterRes, err := limiter.Allow(context.Background(), limiterKey, redis_rate.PerMinute(rateCommentMaxQPM))
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":             err,
				"comment_id":      commentID,
				"comment_content": commentContent,
			}).Errorf("RateComment limiter error")
			logging.SetSpanError(span, err)
			break
		}

		//如果 limiterRes.Allowed 不为零，表示有剩余的配额允许操作
		//note: 调用 RateCommentByGPT 函数来执行对评论内容评分的操作，并且通过 break 跳出循环
		if limiterRes.Allowed != 0 {
			//返回用gpt接口对评论进行的打分和原因
			rate, reason, _ = RateCommentByGPT(commentContent, logger, span)
			break
		}
		logger.WithFields(logrus.Fields{
			"comment_id": commentID,
		}).Debugf("Wait for ChatGPT API rate limit.")
		time.Sleep(20 * time.Second)
	}

	rComment := models.Comment{
		ID:     commentID,
		Rate:   rate,   //分数
		Reason: reason, //原因
	}

	//在数据库中的评论表中更新评论的打分和原因
	result := database.Client.Updates(&rComment)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err":        result.Error,
			"comment_id": commentID,
		}).Errorf("CommentService failed to add comment rate to database")
		logging.SetSpanError(span, result.Error)
	}
	logger.WithFields(logrus.Fields{
		"comment_id": commentID,
		"rate":       rate,
		"reason":     reason,
		//"flagged":    rComment.ModerationFlagged,
	}).Debugf("Add comment rate successfully.")
}

// 输出合法评论数目
func count(ctx context.Context, videoId uint32) (count int64, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "CountComment")
	defer span.End()
	logger := logging.LogService("CommentService.CountComment").WithContext(ctx)

	result := database.Client.Model(&models.Comment{}).WithContext(ctx).
		Where("video_id = ?", videoId).
		Where("rate <= 3").
		//Where("moderation_flagged = false").
		Count(&count)

	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Faild to count comments")
		logging.SetSpanError(span, err)
	}
	return count, result.Error
}

// Put the magic comment to the front
func reindexCommentList(commentList *[]models.Comment) {
	var magicComments []models.Comment
	var commonComments []models.Comment

	for _, c := range *commentList {
		if c.UserId == config.EnvCfg.MagicUserId {
			magicComments = append(magicComments, c)
		} else {
			commonComments = append(commonComments, c)
		}
	}

	*commentList = append(magicComments, commonComments...)
}
