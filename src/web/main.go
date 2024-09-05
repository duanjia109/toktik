package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/profiling"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/web/about"
	"GuGoTik/src/web/auth"
	comment2 "GuGoTik/src/web/comment"
	favorite2 "GuGoTik/src/web/favorite"
	feed2 "GuGoTik/src/web/feed"
	message2 "GuGoTik/src/web/message"
	"GuGoTik/src/web/middleware"
	publish2 "GuGoTik/src/web/publish"
	relation2 "GuGoTik/src/web/relation"
	user2 "GuGoTik/src/web/user"
	"context"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	ginprometheus "github.com/zsais/go-gin-prometheus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"time"
)

func main() {
	// Set Trace Provider
	tp, err := tracing.SetTraceProvider(config.WebServiceName)
	//初始化一个跟踪提供程序（Trace Provider），用于分布式跟踪系统（如OpenTelemetry、Jaeger等）

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

	//step: 配置中间件
	g := gin.Default()
	//step: Configure Prometheus
	p := ginprometheus.NewPrometheus("GuGoTik-WebGateway")
	//step；这行代码将创建的 Prometheus 监控器 p 应用于 Gin 实例 g。这样做的目的是将监控器挂载到 Gin 应用中，使其能够自动记录和暴露与 HTTP 请求相关的指标
	p.Use(g)
	//step：配置Gzip中间件
	g.Use(gzip.Gzip(gzip.DefaultCompression)) //这个中间件的作用是自动将HTTP响应体进行Gzip压缩，然后发送给客户端。
	//step：配置Tracing中间件
	g.Use(otelgin.Middleware(config.WebServiceName)) // Gin 路由引擎添加一个 OpenTelemetry 的中间件，
	// 以便为每个 HTTP 请求收集分布式追踪数据，并使用配置中的服务名称 config.WebServiceName 进行标识。
	//在这个服务运行期间，所有经过 Gin 路由的请求将被自动记录和追踪。
	//step：token校验中间件
	g.Use(middleware.TokenAuthMiddleware())
	//step：限流中间件，最大访问速率为 每秒 1000 个请求
	g.Use(middleware.RateLimiterMiddleWare(time.Second, 1000, 1000))

	//step：Configure Pyroscope
	profiling.InitPyroscope("GuGoTik.GateWay")

	//Test Service
	g.GET("/about", about.Handle)

	// Production Service
	rootPath := g.Group("/douyin")

	//user服务   /douyin/user...
	user := rootPath.Group("/user")
	{
		user.GET("/", user2.UserHandler)             //获取用户信息
		user.POST("/login/", auth.LoginHandle)       //用户登录
		user.POST("/register/", auth.RegisterHandle) //用户注册
	}
	feed := rootPath.Group("/feed")
	{
		feed.GET("/", feed2.ListVideosByRecommendHandle) //获取推荐视频列表
	}
	comment := rootPath.Group("/comment")
	{
		comment.POST("/action/", comment2.ActionCommentHandler) //评论操作：添加/删除
		comment.GET("/list/", comment2.ListCommentHandler)      //列出某video所有的评论
		comment.GET("/count/", comment2.CountCommentHandler)    //列出某video评论数目
	}
	relation := rootPath.Group("/relation")
	{
		//todo: frontend
		relation.POST("/action/", relation2.ActionRelationHandler)        //关注和取消关注操作
		relation.POST("/follow/", relation2.FollowHandler)                //关注
		relation.POST("/unfollow/", relation2.UnfollowHandler)            //取消关注
		relation.GET("/follow/list/", relation2.GetFollowListHandler)     //获取关注列表
		relation.GET("/follower/list/", relation2.GetFollowerListHandler) //获取粉丝列表
		relation.GET("/friend/list/", relation2.GetFriendListHandler)     //获取朋友列表
		relation.GET("/follow/count/", relation2.CountFollowHandler)      //获取关注人数总数
		relation.GET("/follower/count/", relation2.CountFollowerHandler)  //获取粉丝人数总数
		relation.GET("/isFollow/", relation2.IsFollowHandler)             //判断是否存在关注关系
	}

	publish := rootPath.Group("/publish")
	{
		publish.POST("/action/", publish2.ActionPublishHandle) //视频投稿
		publish.GET("/list/", publish2.ListPublishHandle)      //发布列表
	}
	//todo
	message := rootPath.Group("/message")
	{
		message.GET("/chat/", message2.ListMessageHandler)      //列出message列表信息
		message.POST("/action/", message2.ActionMessageHandler) //发消息
	}
	favorite := rootPath.Group("/favorite")
	{
		favorite.POST("/action/", favorite2.ActionFavoriteHandler) //赞操作
		favorite.GET("/list/", favorite2.ListFavoriteHandler)      //获取用户喜欢列表
	}
	// Run Server
	if err := g.Run(config.WebServiceAddr); err != nil {
		panic("Can not run GuGoTik Gateway, binding port: " + config.WebServiceAddr)
	}
}
