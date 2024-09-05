package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/favorite"
	"GuGoTik/src/rpc/publish"
	"GuGoTik/src/rpc/relation"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/cached"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"context"
	"sync"

	"github.com/sirupsen/logrus"
)

type UserServiceImpl struct {
	user.UserServiceServer
}

var relationClient relation.RelationServiceClient

var publishClient publish.PublishServiceClient

var favoriteClient favorite.FavoriteServiceClient

func (a UserServiceImpl) New() {
	relationConn := grpc2.Connect(config.RelationRpcServerName)
	relationClient = relation.NewRelationServiceClient(relationConn)

	publishConn := grpc2.Connect(config.PublishRpcServerName)
	publishClient = publish.NewPublishServiceClient(publishConn)

	favoriteConn := grpc2.Connect(config.FavoriteRpcServerName)
	favoriteClient = favorite.NewFavoriteServiceClient(favoriteConn)
}

// GetUserInfo
//
//	@Description: 根据request.UserId查找用户信息
//	@receiver a
//	@param ctx
//	@param request  含有UserId
//	@return resp
//	@return err
func (a UserServiceImpl) GetUserInfo(ctx context.Context, request *user.UserRequest) (resp *user.UserResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "GetUserInfo")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("UserService.GetUserInfo").WithContext(ctx)

	//step: 缓存中查找
	var userModel models.User
	userModel.ID = request.UserId
	//note: 这里通过三层缓存结构查找用户信息,注意key的前缀为"UserInfo"，
	// 缓存里的结构："UserInfo"+id  -  userinfo(Redis hash)
	ok, err := cached.ScanGet(ctx, "UserInfo", &userModel)

	if err != nil {

		resp = &user.UserResponse{
			StatusCode: strings.UserServiceInnerErrorCode,
			StatusMsg:  strings.UserServiceInnerError,
		}
		return
	}

	if !ok { //不存在该用户，报错
		resp = &user.UserResponse{
			StatusCode: strings.UserNotExistedCode,
			StatusMsg:  strings.UserNotExisted,
			User:       nil,
		}
		logger.WithFields(logrus.Fields{
			"user": request.UserId,
		}).Infof("Do not exist")
		return
	}

	resp = &user.UserResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		User: &user.User{
			Id:              request.UserId,
			Name:            userModel.UserName,
			FollowCount:     nil,
			FollowerCount:   nil,
			IsFollow:        false,
			Avatar:          &userModel.Avatar,
			BackgroundImage: &userModel.BackgroundImage,
			Signature:       &userModel.Signature,
			TotalFavorited:  nil,
			WorkCount:       nil,
			FavoriteCount:   nil,
		},
	}

	//step：6个协程，填充userinfo其他信息
	// 调用其他微服务完成
	var wg sync.WaitGroup
	wg.Add(6)
	isErr := false

	go func() {
		defer wg.Done()
		//step: 在RelationService中找request.UserId的FollowList
		rResp, err := relationClient.CountFollowList(ctx, &relation.CountFollowListRequest{UserId: request.UserId})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get follow list")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get follow list")
				isErr = true
				return
			}
		}

		resp.User.FollowCount = &rResp.Count
	}()

	go func() {
		defer wg.Done()
		//lable： rpc调用 RelationService CountFollowerList
		//step: 在RelationService中找request.UserId的FollowerList
		rResp, err := relationClient.CountFollowerList(ctx, &relation.CountFollowerListRequest{UserId: request.UserId})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get follower list")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get follower list")
				isErr = true
				return
			}
		}

		resp.User.FollowerCount = &rResp.Count
	}()

	go func() {
		defer wg.Done()
		//step: 在RelationService中找User.IsFollow属性
		rResp, err := relationClient.IsFollow(ctx, &relation.IsFollowRequest{ //q:自己被自己关注？
			ActorId: request.ActorId,
			UserId:  request.UserId,
		})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get is follow")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get is follow")
				isErr = true
				return
			}
		}

		resp.User.IsFollow = rResp.Result
	}()

	go func() {
		defer wg.Done()
		//step: 在PublishService中找User.WorkCount（发表的视频总数）
		rResp, err := publishClient.CountVideo(ctx, &publish.CountVideoRequest{UserId: request.UserId})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get published count")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get published count")
				isErr = true
				return
			}
		}

		resp.User.WorkCount = &rResp.Count
	}()

	go func() {
		defer wg.Done()
		//q：这查找的是什么？
		rResp, err := favoriteClient.CountUserTotalFavorited(ctx, &favorite.CountUserTotalFavoritedRequest{
			ActorId: request.ActorId,
			UserId:  request.UserId,
		})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get toal favorited")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get toal favorited")
				isErr = true
				return
			}
		}

		resp.User.TotalFavorited = &rResp.Count
	}()

	go func() {
		defer wg.Done()
		rResp, err := favoriteClient.CountUserFavorite(ctx, &favorite.CountUserFavoriteRequest{UserId: request.UserId})
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":    err,
				"userId": request.UserId,
			}).Errorf("Error when user service get favorite")
			isErr = true
			return
		}

		if rResp != nil && rResp.StatusCode == strings.ServiceOKCode {
			if err != nil {
				logger.WithFields(logrus.Fields{
					"errMsg": rResp.StatusMsg,
					"userId": request.UserId,
				}).Errorf("Error when user service get favorite")
				isErr = true
				return
			}
		}

		resp.User.FavoriteCount = &rResp.Count
	}()

	wg.Wait()

	if isErr {
		resp = &user.UserResponse{
			StatusCode: strings.AuthServiceInnerErrorCode,
			StatusMsg:  strings.AuthServiceInnerError,
		}
		return
	}

	return
}

func (a UserServiceImpl) GetUserExistInformation(ctx context.Context, request *user.UserExistRequest) (resp *user.UserExistResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "GetUserExisted")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("UserService.GetUserExisted").WithContext(ctx)

	var userModel models.User
	userModel.ID = request.UserId
	ok, err := cached.ScanGet(ctx, "UserInfo", &userModel)

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when selecting user info")
		logging.SetSpanError(span, err)
		resp = &user.UserExistResponse{
			StatusCode: strings.UserServiceInnerErrorCode,
			StatusMsg:  strings.UserServiceInnerError,
			Existed:    false,
		}
		return
	}

	if !ok {
		resp = &user.UserExistResponse{
			StatusCode: strings.ServiceOKCode,
			StatusMsg:  strings.ServiceOK,
			Existed:    false,
		}
		logger.WithFields(logrus.Fields{
			"user": request.UserId,
		}).Infof("User do not exist")
		return
	}

	resp = &user.UserExistResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		Existed:    true,
	}
	return
}
