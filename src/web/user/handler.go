package user

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/user"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/web/models"
	"GuGoTik/src/web/utils"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

var userClient user.UserServiceClient

// 使用 gRPC 建立一个客户端连接
//
// init
//
//	@Description: config.UserRpcServerName 是配置中指定的 gRPC 服务器地址。
func init() {
	//userConn 是创建的 gRPC 连接对象。
	userConn := grpc2.Connect(config.UserRpcServerName)
	//创建一个特定服务（UserService）的客户端
	userClient = user.NewUserServiceClient(userConn)
}

func UserHandler(c *gin.Context) {
	var req models.UserReq
	_, span := tracing.Tracer.Start(c.Request.Context(), "UserInfoHandler")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("GateWay.UserInfo").WithContext(c.Request.Context())

	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, models.UserRes{
			StatusCode: strings.GateWayParamsErrorCode,
			StatusMsg:  strings.GateWayParamsError,
		})
		logging.SetSpanError(span, err)
		return
	}

	//step：向UserService服务请求UserInfo
	resp, err := userClient.GetUserInfo(c.Request.Context(), &user.UserRequest{
		UserId:  req.UserId,  //请求url里的userid
		ActorId: req.ActorId, //note： 这是鉴权中间件根据token找到的userid
	})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when gateway get info from UserInfo Service")
		logging.SetSpanError(span, err)
		c.Render(http.StatusOK, utils.CustomJSON{Data: resp, Context: c})
		return
	}

	//这行代码的作用是将 resp 数据以 JSON 格式返回给客户端，并设置 HTTP 状态码为 200（表示成功）
	c.Render(http.StatusOK, utils.CustomJSON{Data: resp, Context: c})
}
