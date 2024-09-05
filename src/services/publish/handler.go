package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/feed"
	"GuGoTik/src/rpc/publish"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/cached"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/file"
	"GuGoTik/src/storage/redis"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/pathgen"
	"GuGoTik/src/utils/rabbitmq"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis_rate/v10"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

type PublishServiceImpl struct {
	publish.PublishServiceServer
}

var conn *amqp.Connection

var channel *amqp.Channel

var FeedClient feed.FeedServiceClient
var userClient user.UserServiceClient

func exitOnError(err error) {
	if err != nil {
		panic(err)
	}
}

func CloseMQConn() {
	if err := conn.Close(); err != nil {
		panic(err)
	}

	if err := channel.Close(); err != nil {
		panic(err)
	}
}

var createVideoLimitKeyPrefix = config.EnvCfg.RedisPrefix + "publish_freq_limit"

const createVideoMaxQPS = 3

// Return redis key to record the amount of CreateVideo query of an actor, e.g., publish_freq_limit-1-1669524458
func createVideoLimitKey(userId uint32) string {
	return fmt.Sprintf("%s-%d", createVideoLimitKeyPrefix, userId)
}

func (a PublishServiceImpl) New() {
	FeedRpcConn := grpc2.Connect(config.FeedRpcServerName)
	FeedClient = feed.NewFeedServiceClient(FeedRpcConn)

	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	userClient = user.NewUserServiceClient(userRpcConn)

	var err error

	conn, err = amqp.Dial(rabbitmq.BuildMQConnAddr())
	exitOnError(err)

	channel, err = conn.Channel()
	exitOnError(err)

	exchangeArgs := amqp.Table{
		"x-delayed-type": "topic",
	}
	err = channel.ExchangeDeclare(
		strings.VideoExchange,
		"x-delayed-message", //"topic",
		true,
		false,
		false,
		false,
		exchangeArgs,
	)
	exitOnError(err)

	_, err = channel.QueueDeclare(
		strings.VideoPicker, //视频信息采集(封面/水印)
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)

	_, err = channel.QueueDeclare(
		strings.VideoSummary,
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)

	err = channel.QueueBind(
		strings.VideoPicker,
		strings.VideoPicker,
		strings.VideoExchange,
		false,
		nil,
	)
	exitOnError(err)

	err = channel.QueueBind(
		strings.VideoSummary,
		strings.VideoSummary,
		strings.VideoExchange,
		false,
		nil,
	)
	exitOnError(err)
}

// ListVideo
//
//	@Description: 返回详细videos信息
//	@receiver a
//	@param ctx
//	@param req
//	@return resp
//	@return err
func (a PublishServiceImpl) ListVideo(ctx context.Context, req *publish.ListVideoRequest) (resp *publish.ListVideoResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ListVideoService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("PublishServiceImpl.ListVideo").WithContext(ctx)

	//step 1：检查用户是否存在
	// Check if user exist
	userExistResp, err := userClient.GetUserExistInformation(ctx, &user.UserExistRequest{
		UserId: req.UserId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Query user existence happens error")
		logging.SetSpanError(span, err)
		resp = &publish.ListVideoResponse{
			StatusCode: strings.UserServiceInnerErrorCode,
			StatusMsg:  strings.UserServiceInnerError,
		}
		return
	}

	//用户不存在
	if !userExistResp.Existed {
		logger.WithFields(logrus.Fields{
			"UserID": req.UserId,
		}).Errorf("User ID does not exist")
		logging.SetSpanError(span, err)
		resp = &publish.ListVideoResponse{
			StatusCode: strings.UserDoNotExistedCode, //返回用户不存在
			StatusMsg:  strings.UserDoNotExisted,
		}
		return
	}

	//step 2：从数据库中的videos表中找到所有userid发布的视频
	var videos []models.Video
	err = database.Client.WithContext(ctx).
		Where("user_id = ?", req.UserId).
		Order("created_at DESC").
		Find(&videos).Error
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Warnf("failed to query video")
		logging.SetSpanError(span, err)
		resp = &publish.ListVideoResponse{
			StatusCode: strings.PublishServiceInnerErrorCode,
			StatusMsg:  strings.PublishServiceInnerError,
		}
		return
	}
	//记录找到的视频id列表
	videoIds := make([]uint32, 0, len(videos))
	for _, video := range videos {
		videoIds = append(videoIds, video.ID)
	}

	//step 3：根据数据库中查到的videoids列表查找详细的VideoList
	queryVideoResp, err := FeedClient.QueryVideos(ctx, &feed.QueryVideosRequest{
		ActorId:  req.ActorId,
		VideoIds: videoIds,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Warnf("queryVideoResp failed to obtain")
		logging.SetSpanError(span, err)
		resp = &publish.ListVideoResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
		}
		return
	}

	logger.WithFields(logrus.Fields{
		"response": resp,
	}).Debug("all process done, ready to launch response")
	//step 4：返回结果
	resp = &publish.ListVideoResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		VideoList:  queryVideoResp.VideoList,
	}
	return
}

// CountVideo
// 查找req.UserId发表的视频总数
//
//	@Description:缓存+数据库 查找
//	@receiver a
//	@param ctx
//	@param req
//	@return resp
//	@return err
func (a PublishServiceImpl) CountVideo(ctx context.Context, req *publish.CountVideoRequest) (resp *publish.CountVideoResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "CountVideoService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("PublishServiceImpl.CountVideo").WithContext(ctx)

	countStringKey := fmt.Sprintf("VideoCount-%d", req.UserId)
	countString, err := cached.GetWithFunc(ctx, countStringKey, //缓存中寻找VideoCount-req.UserId
		func(ctx context.Context, key string) (string, error) {
			rCount, err := count(ctx, req.UserId) //去数据库中找
			return strconv.FormatInt(rCount, 10), err
		})

	if err != nil {
		cached.TagDelete(ctx, "VideoCount")
		logger.WithFields(logrus.Fields{
			"err":     err,
			"user_id": req.UserId,
		}).Errorf("failed to count video")
		logging.SetSpanError(span, err)

		resp = &publish.CountVideoResponse{
			StatusCode: strings.PublishServiceInnerErrorCode,
			StatusMsg:  strings.PublishServiceInnerError,
		}
		return
	}
	rCount, _ := strconv.ParseUint(countString, 10, 64)

	resp = &publish.CountVideoResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Count:      uint32(rCount),
	}
	return
}

// count
// 找userId发表的视频总数
//
//	@Description:在Video表中找所有user_id = userId的记录条数，即userId发表的视频总数
//	@param ctx
//	@param userId
//	@return count
//	@return err
func count(ctx context.Context, userId uint32) (count int64, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "CountVideo")
	defer span.End()
	logger := logging.LogService("PublishService.CountVideo").WithContext(ctx)
	//在Video表中找所有user_id = userId的记录条数，即userId发表的视频总数
	result := database.Client.Model(&models.Video{}).WithContext(ctx).Where("user_id = ?", userId).Count(&count)

	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when counting video")
		logging.SetSpanError(span, err)
	}
	return count, result.Error
}

// CreateVideo
// 用户视频投稿时调用的  /publish/action
//
//	@Description:流程
//	速率限制 -> 格式检测 -> 生成数据，如视频id，原始视频名称，封面名称 ->
//	上传视频到一个地址 -> 向数据库插入一条视频数据 -> 将数据发布到mq：VideoPicker和VideoSummary -> 删除用户的VideoCount缓存
//	@receiver a
//	@param ctx
//	@param request
//	@return resp
//	@return err
func (a PublishServiceImpl) CreateVideo(ctx context.Context, request *publish.CreateVideoRequest) (resp *publish.CreateVideoResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "CreateVideoService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("PublishService.CreateVideo").WithContext(ctx)

	logger.WithFields(logrus.Fields{
		"ActorId": request.ActorId,
		"Title":   request.Title,
	}).Infof("Create video requested.")

	//step：Rate limiting
	// 这段代码使用了 Redis 作为速率限制器来限制视频创建操作的频率
	limiter := redis_rate.NewLimiter(redis.Client)
	limiterKey := createVideoLimitKey(request.ActorId)
	//limiter.Allow: 检查给定的键 limiterKey 是否在指定的频率限制内
	//limiterKey: 限制器键，用于标识特定用户或操作。
	//redis_rate.PerSecond(createVideoMaxQPS): 指定速率限制为每秒允许 createVideoMaxQPS 个请求。
	limiterRes, err := limiter.Allow(ctx, limiterKey, redis_rate.PerSecond(createVideoMaxQPS))
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
		}).Errorf("CreateVideo limiter error")

		resp = &publish.CreateVideoResponse{
			StatusCode: strings.VideoServiceInnerErrorCode,
			StatusMsg:  strings.VideoServiceInnerError,
		}
		return
	}
	//如果速率限制器不允许当前请求（limiterRes.Allowed == 0）,记录日志并返回速率限制的错误响应。
	if limiterRes.Allowed == 0 {
		logger.WithFields(logrus.Fields{
			"err":     err,
			"ActorId": request.ActorId,
		}).Errorf("Create video query too frequently by user %d", request.ActorId)

		resp = &publish.CreateVideoResponse{
			StatusCode: strings.PublishVideoLimitedCode,
			StatusMsg:  strings.PublishVideoLimited,
		}
		return
	}

	//step：检测视频格式，必须是mp4格式
	detectedContentType := http.DetectContentType(request.Data)
	if detectedContentType != "video/mp4" {
		logger.WithFields(logrus.Fields{
			"content_type": detectedContentType,
		}).Debug("invalid content type")
		resp = &publish.CreateVideoResponse{
			StatusCode: strings.InvalidContentTypeCode,
			StatusMsg:  strings.InvalidContentType,
		}
		return
	}
	// byte[] -> reader
	reader := bytes.NewReader(request.Data)

	//step:生成视频名称
	// 创建一个新的随机数生成器，生成视频id
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	videoId := r.Uint32()
	fileName := pathgen.GenerateRawVideoName(request.ActorId, request.Title, videoId) //初始视频名称
	coverName := pathgen.GenerateCoverName(request.ActorId, request.Title, videoId)   //视频封面名称

	//step：上传视频
	_, err = file.Upload(ctx, fileName, reader) //lable:视频保存在本地目录，通过挂载卷实现文件共享之后，存储在nginx中
	if err != nil {
		logger.WithFields(logrus.Fields{
			"file_name": fileName,
			"err":       err,
		}).Debug("failed to upload video")
		resp = &publish.CreateVideoResponse{
			StatusCode: strings.VideoServiceInnerErrorCode,
			StatusMsg:  strings.VideoServiceInnerError,
		}
		return
	}
	logger.WithFields(logrus.Fields{
		"file_name": fileName,
	}).Debug("uploaded video")

	//step；向数据库插入一条视频数据
	//q:如果上传成功，数据库插入失败怎么办？
	raw := &models.RawVideo{
		ActorId:   request.ActorId,
		VideoId:   videoId,
		Title:     request.Title,
		FileName:  fileName,
		CoverName: coverName,
	}
	result := database.Client.Create(&raw)
	if result.Error != nil { //q：这里失败为什么不报错？
		logger.WithFields(logrus.Fields{
			"file_name":  raw.FileName,
			"cover_name": raw.CoverName,
			"err":        err,
		}).Errorf("Error when updating rawVideo information to database")
		logging.SetSpanError(span, result.Error)
	}

	marshal, err := json.Marshal(raw) //用于将结构体或值转换为 JSON 格式的字节流
	if err != nil {
		resp = &publish.CreateVideoResponse{
			StatusCode: strings.VideoServiceInnerErrorCode,
			StatusMsg:  strings.VideoServiceInnerError,
		}
		return
	}

	// Context 注入到 RabbitMQ 中
	headers := rabbitmq.InjectAMQPHeaders(ctx)

	//step:发布到消息队列的两个路由键中
	routingKeys := []string{strings.VideoPicker, strings.VideoSummary}
	for _, key := range routingKeys {
		// Send raw video to all queues bound the exchange
		//note：分别发布到"video_exchange"的两个路由键上：{"video_picker", "video_summary"}
		// 路由键，指定了消息的路由路径。根据交换机的类型和绑定关系，消息将被路由到与路由键匹配的队列。
		err = channel.PublishWithContext(ctx, strings.VideoExchange, key, false, false,
			amqp.Publishing{
				DeliveryMode: amqp.Persistent,
				ContentType:  "text/plain",
				Body:         marshal,
				Headers:      headers,
			})

		if err != nil { //q:上传失败怎么办？
			resp = &publish.CreateVideoResponse{
				StatusCode: strings.VideoServiceInnerErrorCode,
				StatusMsg:  strings.VideoServiceInnerError,
			}
			return
		}
	}

	countStringKey := fmt.Sprintf("VideoCount-%d", request.ActorId)
	//step：删除"VideoCount-$request.ActorId"字符串缓存
	cached.TagDelete(ctx, countStringKey)

	//返回成功
	resp = &publish.CreateVideoResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
	}
	return
}
