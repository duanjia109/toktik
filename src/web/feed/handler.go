package feed

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/feed"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/web/models"
	"GuGoTik/src/web/utils"
	"github.com/gin-gonic/gin"
	_ "github.com/mbobakov/grpc-consul-resolver"
	"github.com/sirupsen/logrus"
	"net/http"
	"strconv"
)

var Client feed.FeedServiceClient

// ListVideosByRecommendHandle
// 用于列出一些视频，具体是通过推荐算法筛选的列表
//
//	@Description: 根据req的请求参数，访问feedService服务中的推荐接口，根据actorId的不同选择不同的rpc服务
//	@param c  actor_id = 匿名用户
func ListVideosByRecommendHandle(c *gin.Context) {
	//step：记录trace和日志
	var req models.ListVideosReq
	_, span := tracing.Tracer.Start(c.Request.Context(), "Feed-ListVideosByRecommendHandle")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//这句代码的作用是创建一个带有特定上下文（context）的日志记录器（logger）
	logger := logging.LogService("GateWay.Videos").WithContext(c.Request.Context()) //通过 WithContext(c.Request.Context()) 将 HTTP 请求的上下文（context）与日志记录器关联。

	//step：参数绑定
	//尝试使用ShouldBindQuery方法将HTTP请求的查询参数绑定到变量req上
	//ShouldBindQuery是Gin框架提供的方法，用于自动解码请求的查询字符串到一个Go结构体中。如果绑定过程中发生错误（例如，查询参数不符合结构体字段的预期格式），err变量将不为nil。
	if err := c.ShouldBindQuery(&req); err != nil {
		//参数绑定有问题
		logger.WithFields(logrus.Fields{
			"latestTime": req.LatestTime,
			"err":        err,
		}).Warnf("Error when trying to bind query")
		c.JSON(http.StatusOK, models.ListVideosRes{
			StatusCode: strings.GateWayParamsErrorCode,
			StatusMsg:  strings.GateWayParamsError,
			NextTime:   nil,
			VideoList:  nil,
		})
		return
	}

	//q：这两个参数的意思？
	latestTime := req.LatestTime
	actorId := uint32(req.ActorId) //note：中间件校验设置的actor_id,表示用户身份，这个url里是匿名用户
	var res *feed.ListFeedResponse
	var err error
	//尝试将字符串 config.EnvCfg.AnonymityUser 转换为一个无符号整数（uint）
	anonymity, err := strconv.ParseUint(config.EnvCfg.AnonymityUser, 10, 32)
	if err != nil {
		c.JSON(http.StatusOK, models.ListVideosRes{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError,
			NextTime:   nil,
			VideoList:  nil,
		})
		return
	}
	//step:id是匿名用户
	if actorId == uint32(anonymity) { //是匿名用户，根据latest_time拉取视频？
		//lable：rpc调用 FeedService  ListVideos
		res, err = Client.ListVideos(c.Request.Context(), &feed.ListFeedRequest{
			LatestTime: &latestTime,
			ActorId:    &actorId,
		})
	} else { //登录用户，根据推荐算法拉取视频
		//lable：rpc调用  feedService  ListVideosByRecommend
		//note:func (r *Request) Context() context.Context 是在 Go 语言的 net/http 包中定义的一个方法，
		// 用于返回与 HTTP 请求相关联的上下文对象（context.Context）。
		// 上下文对象用于在请求的生命周期中传递取消信号、截止日期和其他请求范围内的数据。
		//q：需要重点看看
		res, err = Client.ListVideosByRecommend(c.Request.Context(), &feed.ListFeedRequest{
			LatestTime: &latestTime,
			ActorId:    &actorId,
		})
	}
	//rpc请求出错
	if err != nil {
		logger.WithFields(logrus.Fields{
			"LatestTime": latestTime,
			"Err":        err,
		}).Warnf("Error when trying to connect with FeedService")
		c.JSON(http.StatusOK, models.ListVideosRes{
			StatusCode: strings.FeedServiceInnerErrorCode,
			StatusMsg:  strings.FeedServiceInnerError, //视频服务出现内部错误
			NextTime:   nil,
			VideoList:  nil,
		})
		return
	}

	//成功，返回rpc结果
	//使用 Render 方法可以很方便地返回自定义格式的响应，同时保持代码的清晰和简洁。这是Gin框架提供的一种强大功能，使得Web开发更加灵活和高效。
	//注意：CustomJSON结构体需要实现Render的两个接口
	c.Render(http.StatusOK, utils.CustomJSON{Data: res, Context: c})
}

func init() {
	conn := grpc2.Connect(config.FeedRpcServerName)
	Client = feed.NewFeedServiceClient(conn)
}
