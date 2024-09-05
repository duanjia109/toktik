package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/auth"
	"GuGoTik/src/rpc/recommend"
	"GuGoTik/src/rpc/relation"
	user2 "GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/cached"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/redis"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/willf/bloom"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	stringsLib "strings"
	"sync"
)

var relationClient relation.RelationServiceClient
var userClient user2.UserServiceClient
var recommendClient recommend.RecommendServiceClient

var BloomFilter *bloom.BloomFilter

type AuthServiceImpl struct { //note: 结构体嵌套接口
	auth.AuthServiceServer
}

// New
//
//	@Description: 初始化服务的内部状态或资源，这里主要是其他微服务的客户端初始化
//	@receiver a
func (a AuthServiceImpl) New() {
	relationConn := grpc2.Connect(config.RelationRpcServerName)
	relationClient = relation.NewRelationServiceClient(relationConn)
	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	userClient = user2.NewUserServiceClient(userRpcConn)
	recommendRpcConn := grpc2.Connect(config.RecommendRpcServiceName)
	recommendClient = recommend.NewRecommendServiceClient(recommendRpcConn)
}

// Authenticate
// 鉴权：通过token找到对应的userid
//
//	@Description:主要调用hasToken函数
//	@receiver a
//	@param ctx
//	@param request
//	@return resp 包含结果，是否找到，若找到的userid
//	@return err
func (a AuthServiceImpl) Authenticate(ctx context.Context, request *auth.AuthenticateRequest) (resp *auth.AuthenticateResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "AuthenticateService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("AuthService.Authenticate").WithContext(ctx)

	//step：验证token
	userId, ok, err := hasToken(ctx, request.Token)

	if err != nil {
		resp = &auth.AuthenticateResponse{
			StatusCode: strings.AuthServiceInnerErrorCode,
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	if !ok {
		resp = &auth.AuthenticateResponse{
			StatusCode: strings.UserNotExistedCode,
			StatusMsg:  strings.UserNotExisted,
		}
		return
	}

	//step：验证成功返回id
	id, err := strconv.ParseUint(userId, 10, 32)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":   err,
			"token": request.Token,
		}).Warnf("AuthService Authenticate Action failed to response when parsering uint")
		logging.SetSpanError(span, err)

		resp = &auth.AuthenticateResponse{
			StatusCode: strings.AuthServiceInnerErrorCode,
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	resp = &auth.AuthenticateResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		UserId:     uint32(id),
	}

	return
}

// Register
//
//	@Description:
//	1.判断用户是否存在，已经存在的用户不能再次注册
//
// 2.不存在：生成密码哈希，获取头像，签名，背景图，在数据库插入一条用户数据生成userid（主键），获取/生成用户token，token - userid存入缓存
// rpc推荐服务注册推荐用户，布隆过滤器加入username，在redis上bloom频道发布username，
// 3.让user和magic user互相关注
//
//	@receiver a
//	@param ctx
//	@param request  username password
//	@return resp
//	@return err
func (a AuthServiceImpl) Register(ctx context.Context, request *auth.RegisterRequest) (resp *auth.RegisterResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "RegisterService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("AuthService.Register").WithContext(ctx)

	resp = &auth.RegisterResponse{}
	var user models.User
	//step：查数据库判断用户名是否已经存在？已存在判错
	result := database.Client.WithContext(ctx).Limit(1).Where("user_name = ?", request.Username).Find(&user)
	if result.RowsAffected != 0 {
		resp = &auth.RegisterResponse{
			StatusCode: strings.AuthUserExistedCode,
			StatusMsg:  strings.AuthUserExisted,
		}
		return
	}

	//用户不存在
	var hashedPassword string
	//生成密码哈希
	if hashedPassword, err = hashPassword(ctx, request.Password); err != nil {
		logger.WithFields(logrus.Fields{
			"err":      result.Error,
			"username": request.Username,
		}).Warnf("AuthService Register Action failed to response when hashing password")
		logging.SetSpanError(span, err)

		resp = &auth.RegisterResponse{
			StatusCode: strings.AuthServiceInnerErrorCode, //内部错误
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	//这里起了两个协程
	wg := sync.WaitGroup{}
	wg.Add(2)

	//step：Get Sign  获取用户的随机签名
	go func() {
		defer wg.Done()
		//获取随机短句
		resp, err := http.Get("https://v1.hitokoto.cn/?c=b&encode=text")
		_, span := tracing.Tracer.Start(ctx, "FetchSignature")
		defer span.End()
		logger := logging.LogService("AuthService.FetchSignature").WithContext(ctx)

		if err != nil {
			user.Signature = user.UserName
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Warnf("Can not reach hitokoto")
			logging.SetSpanError(span, err)
			return
		}

		//如果resp.StatusCode != http.StatusOK  将用户签名设置为用户名
		if resp.StatusCode != http.StatusOK {
			user.Signature = user.UserName
			logger.WithFields(logrus.Fields{
				"status_code": resp.StatusCode,
			}).Warnf("Hitokoto service may be error")
			logging.SetSpanError(span, err)
			return
		}

		//读取resp.Body失败  也将用户签名设置为用户名
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			user.Signature = user.UserName
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Warnf("Can not decode the response body of hitokoto")
			logging.SetSpanError(span, err)
			return
		}

		//都无错误 设置签名为随机短句
		user.Signature = string(body)
	}()

	//step： 为用户获取头像（通过邮箱/随机）
	go func() {
		defer wg.Done()
		user.UserName = request.Username
		if user.IsNameEmail() {
			logger.WithFields(logrus.Fields{
				"mail": request.Username,
			}).Infof("Trying to get the user avatar")
			user.Avatar = getAvatarByEmail(ctx, request.Username)
		} else {
			logger.WithFields(logrus.Fields{
				"mail": request.Username,
			}).Infof("Username is not the email, using default logic to fetch avatar")
			user.Avatar = fmt.Sprintf("https://api.multiavatar.com/%s.png", url.QueryEscape(request.Username))
		}
	}()

	wg.Wait()

	user.BackgroundImage = "https://i.mij.rip/2023/08/26/0caa1681f9ae3de38f7d8abcc3b849fc.jpeg"
	user.Password = hashedPassword

	//step：数据库插入用户数据
	result = database.Client.WithContext(ctx).Create(&user) //数据库插入一条user数据，返回主键Id 自增
	if result.Error != nil {
		logger.WithFields(logrus.Fields{
			"err":      result.Error,
			"username": request.Username,
		}).Warnf("AuthService Register Action failed to response when creating user")
		logging.SetSpanError(span, result.Error)

		resp = &auth.RegisterResponse{
			StatusCode: strings.AuthServiceInnerErrorCode, //内部错误
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	//step：id是数据库中的主键，生成用户token并存入
	resp.Token, err = getToken(ctx, user.ID)

	if err != nil {
		resp = &auth.RegisterResponse{
			StatusCode: strings.AuthServiceInnerErrorCode, //内部错误
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	logger.WithFields(logrus.Fields{
		"username": request.Username,
	}).Infof("User register success!")

	//lable：进行rpc调用，recommendResp返回调用结果，err为错误信息
	//step：向推荐系统添加用户
	recommendResp, err := recommendClient.RegisterRecommendUser(ctx, &recommend.RecommendRegisterRequest{UserId: user.ID, Username: request.Username})
	if err != nil || recommendResp.StatusCode != strings.ServiceOKCode {
		resp = &auth.RegisterResponse{
			StatusCode: strings.AuthServiceInnerErrorCode, //内部错误
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	resp.UserId = user.ID
	resp.StatusCode = strings.ServiceOKCode
	resp.StatusMsg = strings.ServiceOK

	//step：添加布隆过滤器
	BloomFilter.AddString(user.UserName)
	logger.WithFields(logrus.Fields{
		"username": user.UserName,
	}).Infof("Publishing user name to redis channel")
	//q：发布消息到Redis的"GuGoTik-Bloom"频道   干什么？加入布隆过滤器，但这里不是已经加过了吗
	//step：Publish the username to redis
	//Publish 方法用于向redis的GuGoTik-Bloom频道发布username消息。
	err = redis.Client.Publish(ctx, config.BloomRedisChannel, user.UserName).Err()
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":      err,
			"username": user.UserName,
		}).Errorf("Publishing user name to redis channel happens error")
		logging.SetSpanError(span, err)
	}

	//step：和magicuser互相关注
	addMagicUserFriend(ctx, &span, user.ID)

	return
}

// Login
//
//	@Description: 根据用户名和密码进行登陆验证，验证通过返回token，没有找到已有token要新建token
//	@receiver a
//	@param ctx
//	@param request
//	@return resp  登陆成功要返回userId和token
//	@return err
func (a AuthServiceImpl) Login(ctx context.Context, request *auth.LoginRequest) (resp *auth.LoginResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "LoginService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("AuthService.Login").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"username": request.Username,
	}).Debugf("User try to log in.")

	// step：Check if a username might be in the filter
	// note：布隆过滤器优化
	if !BloomFilter.TestString(request.Username) { //布隆过滤器说不在就一定不在，直接返回无法登录
		resp = &auth.LoginResponse{
			StatusCode: strings.UnableToQueryUserErrorCode,
			StatusMsg:  strings.UnableToQueryUserError,
		}

		logger.WithFields(logrus.Fields{
			"username": request.Username,
		}).Infof("The user is blocked by Bloom Filter")
		return
	}

	resp = &auth.LoginResponse{}
	user := models.User{ //最后在数据库中进行查找
		UserName: request.Username,
	}
	//step：验证用户名和密码（从cache - redis中读取）
	ok, err := isUserVerifiedInRedis(ctx, request.Username, request.Password)
	if err != nil { //出错了
		resp = &auth.LoginResponse{
			StatusCode: strings.AuthServiceInnerErrorCode,
			StatusMsg:  strings.AuthServiceInnerError,
		}
		logging.SetSpanError(span, err)
		return
	}

	//从数据库里查，验证密码之后，存到redis里
	if !ok { //step: 验证失败
		//在User表中根据user_name查找用户信息记录在user对象中
		result := database.Client.Where("user_name = ?", request.Username).WithContext(ctx).Find(&user)
		if result.Error != nil {
			logger.WithFields(logrus.Fields{
				"err":      result.Error,
				"username": request.Username,
			}).Warnf("AuthService Login Action failed to response with inner err.")
			logging.SetSpanError(span, result.Error)

			resp = &auth.LoginResponse{
				StatusCode: strings.AuthServiceInnerErrorCode,
				StatusMsg:  strings.AuthServiceInnerError,
			}
			logging.SetSpanError(span, err)
			return
		}

		//没找到这个用户的信息，报错"用户不存在"
		if result.RowsAffected == 0 {
			resp = &auth.LoginResponse{
				StatusCode: strings.UserNotExistedCode,
				StatusMsg:  strings.UserNotExisted,
			}
			return
		}

		//用户存在时验证密码，验证失败
		if !checkPasswordHash(ctx, request.Password, user.Password) { //密码对比未通过
			resp = &auth.LoginResponse{
				StatusCode: strings.AuthUserLoginFailedCode,
				StatusMsg:  strings.AuthUserLoginFailed,
			}
			return
		}

		//step：走到这里已经验证密码通过了
		hashed, errs := hashPassword(ctx, request.Password) //哈希用户密码
		if errs != nil {
			logger.WithFields(logrus.Fields{
				"err":      errs,
				"username": request.Username,
			}).Warnf("AuthService Login Action failed to response with inner err.")
			logging.SetSpanError(span, errs)

			resp = &auth.LoginResponse{
				StatusCode: strings.AuthServiceInnerErrorCode,
				StatusMsg:  strings.AuthServiceInnerError,
			}
			logging.SetSpanError(span, err)
			return
		}

		//step:验证通过后说明用户名和密码信息匹配，于是把 "UserLog"+username - password 信息更新到cache和redis中
		if err = setUserInfoToRedis(ctx, user.UserName, hashed); err != nil {
			resp = &auth.LoginResponse{
				StatusCode: strings.AuthServiceInnerErrorCode,
				StatusMsg:  strings.AuthServiceInnerError,
			}
			logging.SetSpanError(span, err)
			return
		}

		//step：把 "UserId"+Username - user.ID 写入缓存
		cached.Write(ctx, fmt.Sprintf("UserId%s", request.Username), strconv.Itoa(int(user.ID)), true)
	} else { //step：验证成功
		//step：成功匹配后还要在cache - redis中查request.Username的UserId
		//q：为什么不关心是否找到？默认一定会找到？
		id, _, err := cached.Get(ctx, fmt.Sprintf("UserId%s", request.Username))
		if err != nil {
			resp = &auth.LoginResponse{
				StatusCode: strings.AuthServiceInnerErrorCode,
				StatusMsg:  strings.AuthServiceInnerError,
			}
			logging.SetSpanError(span, err) //note:在span中设置error和记录
			return nil, err
		}
		//查到uintId为该user的id
		uintId, _ := strconv.ParseUint(id, 10, 32)
		user.ID = uint32(uintId) //id存到这
	}

	//step: 根据user.ID在缓存中获取token，没有就新建
	token, err := getToken(ctx, user.ID)

	if err != nil {
		resp = &auth.LoginResponse{
			StatusCode: strings.AuthServiceInnerErrorCode,
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	logger.WithFields(logrus.Fields{
		"token":  token,
		"userId": user.ID,
	}).Debugf("User log in sucess !")
	resp = &auth.LoginResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		UserId:     user.ID, //step：返回userId
		Token:      token,   //step：登陆成功最终要返回一个token
	}
	return
}

// hashPassword
//
//	@Description: 使用 bcrypt 包生成密码哈希
//	@param ctx
//	@param password
//	@return string  生成的哈希字节切片转换为字符串
//	@return error
func hashPassword(ctx context.Context, password string) (string, error) {
	_, span := tracing.Tracer.Start(ctx, "PasswordHash")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//使用 bcrypt.GenerateFromPassword 函数生成密码哈希。
	//将密码字符串转换为字节切片，并指定哈希的成本参数为 12。成本参数越高，哈希计算的时间越长，安全性也越高。
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12) //
	return string(bytes), err                                       //生成的哈希字节切片转换为字符串并返回
}

// checkPasswordHash
// 比较两个密码是否匹配
//
//	@Description:
//	@param ctx
//	@param password  用户输入的密码
//	@param hash   存储在数据库中的哈希密码
//	@return bool
func checkPasswordHash(ctx context.Context, password, hash string) bool {
	_, span := tracing.Tracer.Start(ctx, "PasswordHashChecked")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//用户输入的密码与存储在数据库中的哈希密码相比较：hash是存储在数据库中的哈希密码，password是用户输入的密码
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// getToken
// 根据userid查找对应的的 token，没有就新建一个token并把"T2U"+token - userid存入二级缓存中
//
//	@Description:因为token时存在缓存中的，因此先用"U2T"+userId 作为key在cache - redis中查找该用户的token是否存在，
//	若找到则返回；若没找到就调用uuid新建一个token，把"U2T"+userId - token 和 "T2U"+token - userId 都回写到cache-redis中
//	@param ctx
//	@param userId  寻找userid为iserId的token
//	@return string  获取到的token
//	@return error
func getToken(ctx context.Context, userId uint32) (string, error) {
	span := trace.SpanFromContext(ctx)
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("AuthService.Login").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"userId": userId,
	}).Debugf("Select for user token")
	//在cache - redis中找 "U2T"+userId 的值作为token，如果没有找到就使用uuid包新建一个字符串作为用户token，
	//lable：并且把"U2T"+userId - token 回写到cache和redis中
	return cached.GetWithFunc(ctx, "U2T"+strconv.FormatUint(uint64(userId), 10),
		func(ctx context.Context, key string) (string, error) {
			span := trace.SpanFromContext(ctx)
			token := uuid.New().String() //q:uuid是什么？
			span.SetAttributes(attribute.String("token", token))
			//lable：把"T2U"+token - userId 存储在cache - redis中
			cached.Write(ctx, "T2U"+token, strconv.FormatUint(uint64(userId), 10), true)
			return token, nil
		})
}

// hasToken
// 通过token找user
//
//	@Description:在缓存-redis的二级缓存中找key为"T2U"+token的值
//	@param ctx
//	@param token
//	@return string token对应的user
//	@return bool 是否找到
//	@return error 相关错误
func hasToken(ctx context.Context, token string) (string, bool, error) {
	return cached.Get(ctx, "T2U"+token)
}

// isUserVerifiedInRedis
//
//	@Description: 从cache - redis 缓存里找"UserLog"+username - password，
//	读出密码，再去和用户输入的密码比较，进行用户的校验
//
// q: 密码存在缓存中？万一丢失了怎么办？
//
//	@param ctx
//	@param username
//	@param password
//	@return bool  当缓存中存在密码且验证成功时返回true，否则全都返回false；
//	包括：异常情况，缓存中没有该用户密码信息，缓存中有该用户信息但密码校验未通过
//	@return error
func isUserVerifiedInRedis(ctx context.Context, username string, password string) (bool, error) {
	//note：这里在缓存中查找用户的哈希密码,没有去数据库中找，因为如果缓存中没有会在上一级去数据库中处理
	pass, ok, err := cached.Get(ctx, "UserLog"+username)
	//note: 缓存里存的是通过 bcrypt 哈希处理过的密码

	if err != nil {
		return false, nil
	}

	if !ok { //缓存中没有，返回false，nil，需要再外部处理
		return false, nil
	}

	if checkPasswordHash(ctx, password, pass) { //密码匹配
		return true, nil
	}

	return false, nil
}

// setUserInfoToRedis
// 把 "UserLog"+username - password 更新cache并写入redis中
//
//	@Description: 先把旧的删除，再把新的写入
//	@param ctx
//	@param username
//	@param password
//	@return error
func setUserInfoToRedis(ctx context.Context, username string, password string) error {
	_, ok, err := cached.Get(ctx, "UserLog"+username)

	if err != nil {
		return err
	}

	if ok {
		cached.TagDelete(ctx, "UserLog"+username)
	}
	cached.Write(ctx, "UserLog"+username, password, true)
	return nil
}

func getAvatarByEmail(ctx context.Context, email string) string {
	ctx, span := tracing.Tracer.Start(ctx, "Auth-GetAvatar")
	defer span.End()
	return fmt.Sprintf("https://cravatar.cn/avatar/%s?d=identicon", getEmailMD5(ctx, email))
}

func getEmailMD5(ctx context.Context, email string) (md5String string) {
	_, span := tracing.Tracer.Start(ctx, "Auth-EmailMD5")
	defer span.End()
	logging.SetSpanWithHostname(span)
	lowerEmail := stringsLib.ToLower(email)
	hashed := md5.New()
	hashed.Write([]byte(lowerEmail))
	md5Bytes := hashed.Sum(nil)
	md5String = hex.EncodeToString(md5Bytes)
	return
}

// addMagicUserFriend
//
//	@Description: 如果magic user存在，则让user和magic user互相关注？（相互follow关系）
//	@param ctx
//	@param span
//	@param userId
func addMagicUserFriend(ctx context.Context, span *trace.Span, userId uint32) {
	logger := logging.LogService("AuthService.Register.AddMagicUserFriend").WithContext(ctx)

	//q:magicuserid是什么？
	isMagicUserExist, err := userClient.GetUserExistInformation(ctx, &user2.UserExistRequest{
		UserId: config.EnvCfg.MagicUserId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"UserId": userId,
			"Err":    err,
		}).Errorf("Failed to check if the magic user exists")
		logging.SetSpanError(*span, err)
		return
	}

	//不存在
	if !isMagicUserExist.Existed {
		logger.WithFields(logrus.Fields{
			"UserId": userId,
		}).Errorf("Magic user does not exist")
		logging.SetSpanError(*span, errors.New("magic user does not exist"))
		return
	}

	// User follow magic user
	_, err = relationClient.Follow(ctx, &relation.RelationActionRequest{
		ActorId: userId,
		UserId:  config.EnvCfg.MagicUserId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"UserId": userId,
			"Err":    err,
		}).Errorf("Failed to follow magic user")
		logging.SetSpanError(*span, err)
		return
	}

	// Magic user follow user
	_, err = relationClient.Follow(ctx, &relation.RelationActionRequest{
		ActorId: config.EnvCfg.MagicUserId,
		UserId:  userId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"UserId": userId,
			"Err":    err,
		}).Errorf("Magic user failed to follow user")
		logging.SetSpanError(*span, err)
		return
	}
}
