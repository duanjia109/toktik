package middleware

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/auth"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"net/http"
	"strconv"
)

var client auth.AuthServiceClient

// TokenAuthMiddleware
//
//	@Description: 根据token验证user是否存在，请求在url或request body中指定token;
//	对该token进行校验之后对token进行校验，校验通过之后设置actor_id = userid透传下去
//	@return gin.HandlerFunc
func TokenAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, span := tracing.Tracer.Start(c.Request.Context(), "AuthMiddleWare")
		defer span.End()
		logging.SetSpanWithHostname(span) //span会记录hostname和podip
		//初始化日志
		logger := logging.LogService("GateWay.AuthMiddleWare").WithContext(ctx)
		span.SetAttributes(attribute.String("url", c.Request.URL.Path))
		//step： 这些url不需要认证
		if c.Request.URL.Path == "/douyin/user/login/" ||
			c.Request.URL.Path == "/douyin/user/register/" ||
			c.Request.URL.Path == "/douyin/comment/list/" ||
			c.Request.URL.Path == "/douyin/publish/list/" ||
			c.Request.URL.Path == "/douyin/favorite/list/" {
			c.Request.URL.RawQuery += "&actor_id=" + config.EnvCfg.AnonymityUser
			span.SetAttributes(attribute.String("mark_url", c.Request.URL.String()))
			logger.WithFields(logrus.Fields{
				"Path": c.Request.URL.Path,
			}).Debugf("Skip Auth with targeted url")
			c.Next()
			return
		}

		var token string
		//step：获取token的不同方式，使用PostForm或Query获取
		if c.Request.URL.Path == "/douyin/publish/action/" {
			token = c.PostForm("token") //用于从 POST 请求的表单数据中获取参数
		} else {
			token = c.Query("token") //用于从URL查询参数中获取参数。
		}

		//step：这些url不需要认证，设置actor_id为匿名用户
		if token == "" && (c.Request.URL.Path == "/douyin/feed/" ||
			c.Request.URL.Path == "/douyin/relation/follow/list/" ||
			c.Request.URL.Path == "/douyin/relation/follower/list/") {
			//note：设置actor_id为匿名用户
			c.Request.URL.RawQuery += "&actor_id=" + config.EnvCfg.AnonymityUser
			span.SetAttributes(attribute.String("mark_url", c.Request.URL.String()))
			logger.WithFields(logrus.Fields{
				"Path": c.Request.URL.Path,
			}).Debugf("Skip Auth with targeted url")
			c.Next()
			return
		}
		span.SetAttributes(attribute.String("token", token))
		//step:验证token
		//lable:rpc调用
		authenticate, err := client.Authenticate(c.Request.Context(), &auth.AuthenticateRequest{Token: token})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Gateway Auth meet trouble")
			span.RecordError(err)
			c.JSON(http.StatusOK, gin.H{
				"status_code": strings.GateWayErrorCode,
				"status_msg":  strings.GateWayError,
			})
			c.Abort()
			return
		}

		if authenticate.StatusCode != 0 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"status_code": strings.AuthUserNeededCode,
				"status_msg":  strings.AuthUserNeeded,
			})
			c.Abort()
			return
		}

		//step： token校验通过，透传：设置 actor_id = userid
		c.Request.URL.RawQuery += "&actor_id=" + strconv.FormatUint(uint64(authenticate.UserId), 10)
		//执行接下来的处理器
		c.Next() //神奇的语句出现了, 没错就是 c.Next(), 所有中间件都有 Request 和 Response 的分水岭, 就是这个 c.Next(), 否则没有办法传递中间件. 我们来看源码:
	}
}

func init() {
	authConn := grpc2.Connect(config.AuthRpcServerName)
	client = auth.NewAuthServiceClient(authConn)
}
