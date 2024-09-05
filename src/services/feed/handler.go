package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/comment"
	"GuGoTik/src/rpc/favorite"
	"GuGoTik/src/rpc/feed"
	"GuGoTik/src/rpc/recommend"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/cached"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/file"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/rabbitmq"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type FeedServiceImpl struct {
	feed.FeedServiceServer
}

const (
	VideoCount = 3
)

var UserClient user.UserServiceClient
var CommentClient comment.CommentServiceClient
var FavoriteClient favorite.FavoriteServiceClient
var RecommendClient recommend.RecommendServiceClient

var conn *amqp.Connection

var channel *amqp.Channel

func exitOnError(err error) {
	if err != nil {
		panic(err)
	}
}

func (s FeedServiceImpl) New() {
	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	UserClient = user.NewUserServiceClient(userRpcConn)
	commentRpcConn := grpc2.Connect(config.CommentRpcServerName)
	CommentClient = comment.NewCommentServiceClient(commentRpcConn)
	favoriteRpcConn := grpc2.Connect(config.FavoriteRpcServerName)
	FavoriteClient = favorite.NewFavoriteServiceClient(favoriteRpcConn)
	recommendRpcConn := grpc2.Connect(config.RecommendRpcServiceName)
	RecommendClient = recommend.NewRecommendServiceClient(recommendRpcConn)

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

// produceFeed
// 将event发布到 "event" - "video.get.action"
//
//	@Description:
//	@param ctx  ctx（上下文）是一个 context.Context 对象，用于在不同的步骤之间传递元数据和控制信息
//	q:需要了解ctx的作用
//	@param event  要发布到rabbitmq的数据
func produceFeed(ctx context.Context, event models.RecommendEvent) {
	ctx, span := tracing.Tracer.Start(ctx, "FeedPublisher")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.FeedPublisher").WithContext(ctx)
	data, err := json.Marshal(event)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when marshal the event model")
		logging.SetSpanError(span, err)
		return
	}

	headers := rabbitmq.InjectAMQPHeaders(ctx)

	//q：rabbitmq用法
	// 将消息发布到 RabbitMQ
	err = channel.PublishWithContext(ctx,
		strings.EventExchange, //exchangeName：指定消息发布的目标交换机。交换机决定了消息的路由策略
		strings.VideoGetEvent, //routingKey：VideoGetEvent 是用于将消息路由到特定队列的键。
		false,                 //mandatory 标志。如果设置为 true 且没有队列绑定到指定的交换机和路由键，那么消息将返回给发布者。如果设置为 false，则消息会被丢弃
		false,                 //immediate 标志。如果设置为 true 且消息无法立即投递到消费者，消息将返回给发布者。一般来说，现代 RabbitMQ 中这个标志不常使用，且官方建议避免使用。
		amqp.Publishing{ //amqp.Publishing 结构体,定义了消息的属性和内容：
			ContentType: "text/plain", //消息的内容类型，指定消息的 MIME 类型为 "text/plain"
			Body:        data,         //消息的主体，包含实际要发送的数据
			Headers:     headers,      //消息头部信息，从上下文中提取并注入的 AMQP 头部。这些头部可能包含额外的元数据或追踪信息。
		})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when publishing the event model")
		logging.SetSpanError(span, err)
		return
	}
}

// ListVideosByRecommend
//
//	根据推荐算法拉取视频
//	@Description: 更新lastesttime，从推荐算法获取推荐视频数据，再从数据库中查到对应视频数据，
//	进一步获取视频详细信息，把相关数据发布到rabbitmq，最后返回结果
//	@receiver s
//	@param ctx
//	@param request 请求参数
//	@return resp  主要包括视频详细信息
//	@return err
func (s FeedServiceImpl) ListVideosByRecommend(ctx context.Context, request *feed.ListFeedRequest) (resp *feed.ListFeedResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "ListVideosService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.ListVideos").WithContext(ctx)

	now := time.Now().UnixMilli()
	latestTime := now
	//确保 LatestTime 是一个有效的非空指针
	//解引用request.LatestTime到latestTime
	if request.LatestTime != nil && *request.LatestTime != "" && *request.LatestTime != "0" {
		// Check if request.LatestTime is a timestamp
		//检查解引用后的 LatestTime 是否是一个Unix毫秒时间戳。这个函数应该返回两个值：t 是转换后的时间值，ok 是一个布尔值，表示时间戳是否有效。
		t, ok := isUnixMilliTimestamp(*request.LatestTime)
		if ok {
			latestTime = t
		} else {
			logger.WithFields(logrus.Fields{
				"latestTime": request.LatestTime,
			}).Errorf("The latestTime is not a unix timestamp")
			logging.SetSpanError(span, errors.New("the latestTime is not a unit timestamp"))
		}
	}
	//有任何问题默认使用latestTime := now
	//lable：rpc调用 视频推荐微服务函数
	//step：根据推荐服务拿到视频id
	recommendResponse, err := RecommendClient.GetRecommendInformation(ctx, &recommend.RecommendRequest{
		UserId: *request.ActorId,
		Offset: -1,
		Number: VideoCount,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":             err,
			"recommenVideoId": recommendResponse.VideoList,
		}).Errorf("Error when trying to connect with RecommendService")
		logging.SetSpanError(span, err)
		resp = &feed.ListFeedResponse{
			StatusCode: strings.RecommendServiceInnerErrorCode,
			StatusMsg:  strings.RecommendServiceInnerError,
			NextTime:   nil,
			VideoList:  nil,
		}
		return resp, err
	}
	recommendVideoId := recommendResponse.VideoList
	//step：访问数据库拿到视频信息
	find, err := findRecommendVideos(ctx, recommendVideoId)

	nextTimeStamp := uint64(latestTime)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"find": find,
		}).Warnf("func findRecommendVideos meet trouble.")
		logging.SetSpanError(span, err)

		resp = &feed.ListFeedResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			NextTime:   &nextTimeStamp,
			VideoList:  nil,
		}
		return resp, err
	}
	//没找到推荐视频
	if len(find) == 0 {
		resp = &feed.ListFeedResponse{
			StatusCode: strings.ServiceOKCode,
			StatusMsg:  strings.ServiceOK,
			NextTime:   nil,
			VideoList:  nil,
		}
		return resp, err
	}

	var actorId uint32 = 0
	if request.ActorId != nil {
		actorId = *request.ActorId
	}
	//step：获取详细视频信息
	videos := queryDetailed(ctx, logger, actorId, find) //videos存有详细信息
	if videos == nil {
		logger.WithFields(logrus.Fields{
			"videos": videos,
		}).Warnf("func queryDetailed meet trouble.")
		logging.SetSpanError(span, err)
		resp = &feed.ListFeedResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			NextTime:   nil,
			VideoList:  nil,
		}
		return resp, err
	}
	wg := sync.WaitGroup{}
	wg.Add(1)

	//step：发布数据到rabbitmq
	go func() {
		defer wg.Done()
		var videoLists []uint32
		for _, item := range videos {
			//videoLists存储推荐视频id
			videoLists = append(videoLists, item.Id)
		}
		//lable：把此次推荐的RecommendEvent发布到mq
		//q：有什么用？
		produceFeed(ctx, models.RecommendEvent{
			ActorId: *request.ActorId,
			VideoId: videoLists,
			Type:    1,
			Source:  config.FeedRpcServerName,
		})
	}()
	wg.Wait()
	resp = &feed.ListFeedResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		NextTime:   &nextTimeStamp,
		VideoList:  videos,
	}
	return resp, err
}

// ListVideos
//
//	@Description: 匿名用户拉取推荐视频的接口
//	@receiver s
//	@param ctx  携带trace的上下文
//	@param request
//	@return resp
//	@return err
func (s FeedServiceImpl) ListVideos(ctx context.Context, request *feed.ListFeedRequest) (resp *feed.ListFeedResponse, err error) {
	//step：记录trace和日志
	ctx, span := tracing.Tracer.Start(ctx, "ListVideosService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.ListVideos").WithContext(ctx)

	now := time.Now().UnixMilli()
	latestTime := now
	if request.LatestTime != nil && *request.LatestTime != "" {
		// Check if request.LatestTime is a timestamp
		t, ok := isUnixMilliTimestamp(*request.LatestTime)
		if ok {
			latestTime = t
		} else {
			logger.WithFields(logrus.Fields{
				"latestTime": request.LatestTime,
			}).Errorf("The latestTime is not a unix timestamp")
			logging.SetSpanError(span, errors.New("the latestTime is not a unit timestamp"))
		}
	}

	find, nextTime, err := findVideos(ctx, latestTime)
	nextTimeStamp := uint64(nextTime.UnixMilli())
	if err != nil {
		logger.WithFields(logrus.Fields{
			"find": find,
		}).Warnf("func findVideos meet trouble.")
		logging.SetSpanError(span, err)

		resp = &feed.ListFeedResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			NextTime:   &nextTimeStamp,
			VideoList:  nil,
		}
		return resp, err
	}
	if len(find) == 0 {
		resp = &feed.ListFeedResponse{
			StatusCode: strings.ServiceOKCode,
			StatusMsg:  strings.ServiceOK,
			NextTime:   nil,
			VideoList:  nil,
		}
		return resp, err
	}

	var actorId uint32 = 0
	if request.ActorId != nil {
		actorId = *request.ActorId
	}
	videos := queryDetailed(ctx, logger, actorId, find)
	if videos == nil {
		logger.WithFields(logrus.Fields{
			"videos": videos,
		}).Warnf("func queryDetailed meet trouble.")
		logging.SetSpanError(span, err)
		resp = &feed.ListFeedResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			NextTime:   nil,
			VideoList:  nil,
		}
		return resp, err
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var videoLists []uint32
		for _, item := range videos {
			videoLists = append(videoLists, item.Id)
		}
		produceFeed(ctx, models.RecommendEvent{
			ActorId: *request.ActorId,
			VideoId: videoLists,
			Type:    1,
			Source:  config.FeedRpcServerName,
		})
	}()
	wg.Wait()
	resp = &feed.ListFeedResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		NextTime:   &nextTimeStamp,
		VideoList:  videos,
	}
	return resp, err
}

// QueryVideos
// 根据req的参数获取了video的详细信息列表[]*feed.Video
//
//	@Description:主要是调用query函数获取[]*feed.Video列表信息
//	@receiver s
//	@param ctx
//	@param req
//	@return resp 获取了 []*Video 列表，存储video的详细信息
//	@return err
func (s FeedServiceImpl) QueryVideos(ctx context.Context, req *feed.QueryVideosRequest) (resp *feed.QueryVideosResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "QueryVideosService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.QueryVideos").WithContext(ctx)

	//note:调用query函数
	rst, err := query(ctx, logger, req.ActorId, req.VideoIds)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"rst": rst,
		}).Warnf("func query meet trouble.")
		logging.SetSpanError(span, err)
		resp = &feed.QueryVideosResponse{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			VideoList:  rst,
		}
		return resp, err
	}

	resp = &feed.QueryVideosResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		VideoList:  rst,
	}
	return resp, err
}

func (s FeedServiceImpl) QueryVideoExisted(ctx context.Context, req *feed.VideoExistRequest) (resp *feed.VideoExistResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "QueryVideoExistedService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.QueryVideoExisted").WithContext(ctx)
	var video models.Video

	//note：先在缓存中找，找不到去数据库中找
	_, err = cached.GetWithFunc(ctx, fmt.Sprintf("VideoExistedCached-%d", req.VideoId), func(ctx context.Context, key string) (string, error) {
		row := database.Client.WithContext(ctx).Where("id = ?", req.VideoId).First(&video)
		if row.Error != nil {
			return "false", row.Error
		}
		return "true", nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//没找到
			logger.WithFields(logrus.Fields{
				"video_id": req.VideoId,
			}).Warnf("gorm.ErrRecordNotFound")
			logging.SetSpanError(span, err)
			resp = &feed.VideoExistResponse{
				StatusCode: strings.ServiceOKCode,
				StatusMsg:  strings.ServiceOK,
				Existed:    false,
			}
			return resp, nil
		} else {
			//查找过程出错
			logger.WithFields(logrus.Fields{
				"video_id": req.VideoId,
			}).Warnf("Error occurred while querying database")
			logging.SetSpanError(span, err)
			resp = &feed.VideoExistResponse{
				StatusCode: strings.FeedServiceInnerErrorCode,
				StatusMsg:  strings.FeedServiceInnerError,
				Existed:    false,
			}
			return resp, err
		}
	}
	//找到了
	resp = &feed.VideoExistResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Existed:    true,
	}
	return
}

func (s FeedServiceImpl) QueryVideoSummaryAndKeywords(ctx context.Context, req *feed.QueryVideoSummaryAndKeywordsRequest) (resp *feed.QueryVideoSummaryAndKeywordsResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "QueryVideoSummaryAndKeywordsService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FeedService.QueryVideoSummaryAndKeywords").WithContext(ctx)

	videoExistRes, err := s.QueryVideoExisted(ctx, &feed.VideoExistRequest{
		VideoId: req.VideoId,
	})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"VideoId": req.VideoId,
		}).Errorf("Cannot check if the video exists")
		logging.SetSpanError(span, err)

		resp = &feed.QueryVideoSummaryAndKeywordsResponse{
			StatusCode: strings.VideoServiceInnerErrorCode,
			StatusMsg:  strings.VideoServiceInnerError,
		}
		return
	}

	if !videoExistRes.Existed {
		resp = &feed.QueryVideoSummaryAndKeywordsResponse{
			StatusCode: strings.UnableToQueryVideoErrorCode,
			StatusMsg:  strings.UnableToQueryVideoError,
		}
		return
	}

	video := models.Video{}
	result := database.Client.WithContext(ctx).Where("id = ?", req.VideoId).First(&video)
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"VideoId": req.VideoId,
		}).Errorf("Cannot get video from database")
		logging.SetSpanError(span, err)

		resp = &feed.QueryVideoSummaryAndKeywordsResponse{
			StatusCode: strings.VideoServiceInnerErrorCode,
			StatusMsg:  strings.VideoServiceInnerError,
		}
		return
	}

	resp = &feed.QueryVideoSummaryAndKeywordsResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Summary:    video.Summary,
		Keywords:   video.Keywords,
	}

	return
}

// findVideos
//
//	@Description: 从数据库中查找创建时间在latestTime之前的视频
//	@param ctx
//	@param latestTime
//	@return []*models.Video
//	@return time.Time  创建时间最早的时间
//	@return error
func findVideos(ctx context.Context, latestTime int64) ([]*models.Video, time.Time, error) {
	//step： 这里只记录了日志
	//q：为什么没有记录span？
	logger := logging.LogService("ListVideos.findVideos").WithContext(ctx)

	nextTime := time.UnixMilli(latestTime)

	var videos []*models.Video
	result := database.Client.Where("created_at < ?", nextTime).
		Order("created_at DESC").
		Limit(VideoCount).
		Find(&videos)

	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"videos": videos,
		}).Warnf("database.Client.Where meet trouble")
		return nil, nextTime, result.Error
	}

	if len(videos) != 0 {
		nextTime = videos[len(videos)-1].CreatedAt
	}

	logger.WithFields(logrus.Fields{
		"latestTime":  time.UnixMilli(latestTime),
		"VideosCount": len(videos),
		"NextTime":    nextTime,
	}).Debugf("Find videos")
	return videos, nextTime, nil
}

// findRecommendVideos
//
//	@Description: 根据recommendVideoId从数据库中找到video数据
//	@param ctx
//	@param recommendVideoId  推荐视频的id切片数组
//	@return []*models.Video  从数据库中查到的数据
//	@return error
func findRecommendVideos(ctx context.Context, recommendVideoId []uint32) ([]*models.Video, error) {
	logger := logging.LogService("ListVideos.findVideos").WithContext(ctx)
	var videos []*models.Video
	var ids []interface{}
	for _, id := range recommendVideoId {
		ids = append(ids, id)
	}
	result := database.Client.WithContext(ctx).Where("id IN ?", ids).Find(&videos)

	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"videos": videos,
		}).Warnf("database.Client.Where meet trouble")
		return nil, result.Error
	}

	return videos, nil
}

// 创建respVideoList []*feed.Video
//
//	@Description: 根据videos通过一些微服务和file函数的调用完成respVideoList[i]中每一个视频的详细信息
//	@param ctx
//	@param logger
//	@param actorId  q：这个参数是什么意思？request.ActorId  表示请求者的身份？
//	@param videos  从数据库中查到的video列表
//	@return respVideoList   处理过的新videolist，里面有更详细的video信息
func queryDetailed(ctx context.Context, logger *logrus.Entry, actorId uint32, videos []*models.Video) (respVideoList []*feed.Video) {
	ctx, span := tracing.Tracer.Start(ctx, "queryDetailed")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger = logging.LogService("ListVideos.queryDetailed").WithContext(ctx)
	respVideoList = make([]*feed.Video, len(videos))

	//step：Init respVideoList
	for i, v := range videos {
		respVideoList[i] = &feed.Video{
			Id:     v.ID,
			Title:  v.Title,
			Author: &user.User{Id: v.UserId},
		}
	}

	//step： Create userid -> user map to reduce duplicate user info query
	userMap := make(map[uint32]*user.User)
	for _, video := range videos {
		userMap[video.UserId] = &user.User{} //usermap存储videos中每一个视频的userId
	}

	userWg := sync.WaitGroup{}
	userWg.Add(len(userMap))
	//为usermap中的每一个userid找到对应的*user
	for userId := range userMap {
		go func(userId uint32) {
			defer userWg.Done()
			userResponse, localErr := UserClient.GetUserInfo(ctx, &user.UserRequest{
				UserId:  userId,
				ActorId: actorId,
			})
			if localErr != nil || userResponse.StatusCode != strings.ServiceOKCode {
				logger.WithFields(logrus.Fields{
					"UserId": userId,
					"cause":  localErr,
				}).Warning("failed to get user info")
				logging.SetSpanError(span, localErr)
			}
			userMap[userId] = userResponse.User
		}(userId)
	}

	//step：
	wg := sync.WaitGroup{}
	for i, v := range videos {
		wg.Add(4)
		//step：fill play url  在文件存储中的url
		go func(i int, v *models.Video) {
			defer wg.Done()
			//返回一个url的参数部分："http://192.168.124.33:8066/$v.FileName?user_id=$userId"
			//"%s?user_id=%d", originLink, userId
			playUrl, localErr := file.GetLink(ctx, v.FileName, v.UserId)
			if localErr != nil {
				logger.WithFields(logrus.Fields{
					"video_id":  v.ID,
					"file_name": v.FileName,
					"err":       localErr,
				}).Warning("failed to fetch play url")
				logging.SetSpanError(span, localErr)
				return
			}
			respVideoList[i].PlayUrl = playUrl
		}(i, v)

		//step：fill cover url
		go func(i int, v *models.Video) {
			defer wg.Done()
			//eg:"http://192.168.124.33:8066/$v.CoverName?user_id=$userId"
			coverUrl, localErr := file.GetLink(ctx, v.CoverName, v.UserId)
			if localErr != nil {
				logger.WithFields(logrus.Fields{
					"video_id":   v.ID,
					"cover_name": v.CoverName,
					"err":        localErr,
				}).Warning("failed to fetch cover url")
				logging.SetSpanError(span, localErr)
				return
			}
			respVideoList[i].CoverUrl = coverUrl
		}(i, v)

		//step：fill favorite count  填充该视频的喜欢个数
		go func(i int, v *models.Video) {
			defer wg.Done()
			favoriteCount, localErr := FavoriteClient.CountFavorite(ctx, &favorite.CountFavoriteRequest{
				VideoId: v.ID,
			})
			if localErr != nil {
				logger.WithFields(logrus.Fields{
					"video_id": v.ID,
					"err":      localErr,
				}).Warning("failed to fetch favorite count")
				logging.SetSpanError(span, localErr)
				return
			}
			respVideoList[i].FavoriteCount = favoriteCount.Count
		}(i, v)

		//step：fill comment count  总评论数
		//q:获取视频评论数？
		go func(i int, v *models.Video) {
			defer wg.Done()
			commentCount, localErr := CommentClient.CountComment(ctx, &comment.CountCommentRequest{
				ActorId: actorId,
				VideoId: v.ID,
			})
			if localErr != nil {
				logger.WithFields(logrus.Fields{
					"video_id": v.ID,
					"err":      localErr,
				}).Warning("failed to fetch comment count")
				logging.SetSpanError(span, localErr)
				return
			}
			respVideoList[i].CommentCount = commentCount.CommentCount
		}(i, v)

		//step：fill is favorite  填充actorId是否喜欢视频
		//q:什么意思？
		if actorId != 0 {
			wg.Add(1)
			go func(i int, v *models.Video) {
				defer wg.Done()
				isFavorite, localErr := FavoriteClient.IsFavorite(ctx, &favorite.IsFavoriteRequest{
					ActorId: actorId,
					VideoId: v.ID,
				})
				if localErr != nil {
					logger.WithFields(logrus.Fields{
						"video_id": v.ID,
						"err":      localErr,
					}).Warning("failed to fetch favorite status")
					logging.SetSpanError(span, localErr)
					return
				}
				//lable：填充 IsFavorite属性
				respVideoList[i].IsFavorite = isFavorite.Result
			}(i, v)
		} else {
			respVideoList[i].IsFavorite = false
		}
	}
	userWg.Wait()
	wg.Wait()

	for i, respVideo := range respVideoList {
		authorId := respVideo.Author.Id
		respVideoList[i].Author = userMap[authorId]
	}

	return
}

// query
// 先查数据库，再调用ueryDetailed通过一些微服务和file函数获得video详细信息
//
//	@Description:
//	@param ctx
//	@param logger
//	@param actorId
//	@param videoIds
//	@return resp
//	@return err
func query(ctx context.Context, logger *logrus.Entry, actorId uint32, videoIds []uint32) (resp []*feed.Video, err error) {
	var videos []*models.Video
	//Gorm的操作，以后不需要在单独开span，通过传ctx的方式完成 "WithContext(ctx)"，如果在函数需要这样写，但是这个的目的是为了获取子 Span 的 ctx
	err = database.Client.WithContext(ctx).Where("Id IN ?", videoIds).Find(&videos).Error
	if err != nil {
		return nil, err
	}
	return queryDetailed(ctx, logger, actorId, videos), nil
}

func isUnixMilliTimestamp(s string) (int64, bool) {
	timestamp, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}

	startTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	endTime := time.Now().AddDate(100, 0, 0)

	t := time.UnixMilli(timestamp)
	res := t.After(startTime) && t.Before(endTime)

	return timestamp, res
}
