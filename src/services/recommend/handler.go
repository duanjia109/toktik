package main

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/gorse"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/rpc/recommend"
	"GuGoTik/src/storage/redis"
	"GuGoTik/src/utils/logging"
	"context"
	"fmt"
	redis2 "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"strconv"
)

type RecommendServiceImpl struct {
	recommend.RecommendServiceServer
}

func (a RecommendServiceImpl) New() {
	gorseClient = gorse.NewGorseClient(config.EnvCfg.GorseAddr, config.EnvCfg.GorseApiKey)
}

var gorseClient *gorse.GorseClient

// GetRecommendInformation
//
//	@Description:根据request的参数获取推荐视频id，使用gorseClient调用了gorse的接口
//	@receiver a
//	@param ctx
//	@param request  请求参数，rpc调用 -> gorse中都会用到一些参数
//	@return resp  含有返回的推荐视频id列表
//	@return err
func (a RecommendServiceImpl) GetRecommendInformation(ctx context.Context, request *recommend.RecommendRequest) (resp *recommend.RecommendResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "GetRecommendService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("RecommendService.GetRecommend").WithContext(ctx)

	var offset int
	//q：offset这个变量是什么意思？
	if request.Offset == -1 { //step：offset为-1的时候请求完getVideoIds直接返回
		ids, err := getVideoIds(ctx, strconv.Itoa(int(request.UserId)), int(request.Number))

		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when getting recommend user item with default logic")
			logging.SetSpanError(span, err)
			resp = &recommend.RecommendResponse{
				StatusCode: strings.RecommendServiceInnerErrorCode,
				StatusMsg:  strings.RecommendServiceInnerError,
				VideoList:  nil,
			}
			return resp, err
		}

		resp = &recommend.RecommendResponse{
			StatusCode: strings.ServiceOKCode,
			StatusMsg:  strings.ServiceOK,
			VideoList:  ids,
		}
		return resp, nil

	} else { //step： offset不为-1的时候更新offset
		offset = int(request.Offset)
	}

	//step：根据offset值调用gorseClient获取推荐视频id
	videos, err :=
		gorseClient.GetItemRecommend(ctx, strconv.Itoa(int(request.UserId)), []string{}, "read", "5m", int(request.Number), offset)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when getting recommend user item")
		logging.SetSpanError(span, err)
		resp = &recommend.RecommendResponse{
			StatusCode: strings.RecommendServiceInnerErrorCode,
			StatusMsg:  strings.RecommendServiceInnerError,
			VideoList:  nil,
		}
		return
	}

	//step：把videos中的id添加到videoIds中
	var videoIds []uint32
	for _, id := range videos {
		parseUint, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when getting recommend user item")
			logging.SetSpanError(span, err)
			resp = &recommend.RecommendResponse{
				StatusCode: strings.RecommendServiceInnerErrorCode,
				StatusMsg:  strings.RecommendServiceInnerError,
				VideoList:  nil,
			}
			return resp, err
		}
		videoIds = append(videoIds, uint32(parseUint))
	}

	logger.WithFields(logrus.Fields{
		"offset":   offset,
		"videoIds": videoIds,
	}).Infof("Get recommend with offset")
	resp = &recommend.RecommendResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
		VideoList:  videoIds,
	}
	return
}

// RegisterRecommendUser
//
//	@Description: 向推荐系统添加用户
//	@receiver a
//	@param ctx
//	@param request
//	@return resp
//	@return err
func (a RecommendServiceImpl) RegisterRecommendUser(ctx context.Context, request *recommend.RecommendRegisterRequest) (resp *recommend.RecommendRegisterResponse, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "RegisterRecommendService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("RecommendService.RegisterRecommend").WithContext(ctx)

	//step：将用户数据插入到 Gorse 推荐系统中
	//
	_, err = gorseClient.InsertUsers(ctx, []gorse.User{
		{
			UserId:  strconv.Itoa(int(request.UserId)),
			Comment: request.Username,
		},
	})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when creating recommend user")
		logging.SetSpanError(span, err)
		resp = &recommend.RecommendRegisterResponse{
			StatusCode: strings.RecommendServiceInnerErrorCode, //推荐系统内部错误
			StatusMsg:  strings.RecommendServiceInnerError,
		}
		return
	}

	resp = &recommend.RecommendRegisterResponse{
		StatusCode: strings.ServiceOKCode,
		StatusMsg:  strings.ServiceOK,
	}
	return
}

// getVideoIds
//
//	@Description: 从 Gorse 推荐系统中获取一组推荐视频 ID，并确保这些 ID 在 Redis 缓存中没有出现过
//	@param ctx
//	@param actorId  userid，由开始的鉴权中间件设置，或为token对应的userid，或为匿名用户的默认id
//	@param num  表示每次从 Gorse 推荐系统请求获取的视频 ID 的数量。
//	@return ids  通过Gorse开源系统获取的推荐视频id列表
//	@return err
func getVideoIds(ctx context.Context, actorId string, num int) (ids []uint32, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "GetRecommendAutoService")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("RecommendService.GetRecommendAuto").WithContext(ctx)
	key := fmt.Sprintf("%s-RecommendAutoService-%s", config.EnvCfg.RedisPrefix, actorId)
	offset := 0 //q: 对这个参数的意思有点不是很懂？

	for len(ids) < num {
		//lable: 请求gorseClient获取推荐视频id
		//q；为什么不传categories []string参数，用户画像在哪里体现？
		vIds, err := gorseClient.GetItemRecommend(ctx, actorId, []string{}, "read", "5m", num, offset)
		logger.WithFields(logrus.Fields{
			"vIds": vIds,
		}).Debugf("Fetch data from Gorse")

		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":     err,
				"actorId": actorId,
				"num":     num,
			}).Errorf("Error when getting item recommend")
			return nil, err
		}

		//遍历返回的每一个推荐视频id，选择所有没有在redis中出现过的id填充ids
		for _, id := range vIds {
			//lable：主要这个函数，跟reids相关
			// SIsMember 是一个 Redis 命令，用于判断一个成员（member）是否存在于集合（set）
			// key：是要检查的 Redis集合的键名,即名为key的set集合 ，id：是要检查是否存在于集合中的成员
			// 对于SIsMember 命令，返回的数据是一个布尔值，表示 id 是否是 key 集合的成员。
			// 如果 res.Val() 返回 true，则表示 id 存在于 key 集合中。
			// 果 res.Val() 返回 false，则表示 id 不存在于 key 集合中。
			//检查id是否在redis的key集合里
			res := redis.Client.SIsMember(ctx, key, id)
			if res.Err() != nil && res.Err() != redis2.Nil {
				logger.WithFields(logrus.Fields{
					"err":     err,
					"actorId": actorId,
					"num":     num,
				}).Errorf("Error when getting item recommend")
				return nil, err
			}

			logger.WithFields(logrus.Fields{
				"id":  id,
				"res": res,
			}).Debugf("Get id in redis information")

			if !res.Val() { //id不存在，说明不重复
				uintId, err := strconv.ParseUint(id, 10, 32)
				if err != nil {
					logger.WithFields(logrus.Fields{
						"err":     err,
						"actorId": actorId,
						"num":     num,
						"uint":    id,
					}).Errorf("Error when parsing uint")
					return nil, err
				}
				//把没有存过的视频id放到ids集合中，盲猜是在防止重复推荐
				ids = append(ids, uint32(uintId))
			}
		}

		var idsStr []interface{}

		//把ids加入到idsStr切片中
		for _, id := range ids {
			//strconv.FormatUint转换为string
			idsStr = append(idsStr, strconv.FormatUint(uint64(id), 10))
		}

		logger.WithFields(logrus.Fields{
			"actorId": actorId,
			"ids":     idsStr,
		}).Infof("Get recommend information")

		//把idsStr全都加入到redis的set中
		if len(idsStr) != 0 {
			//lable
			//redis集合操作：key为redis中的集合名，ids包含要添加到集合中的成员的字符串切片
			//note: redis.Client.SAdd 函数在向 Redis 集合（Set）中添加元素时，会自动去重。Redis 的集合是一种无序的、不重复的元素集合，这意味着即使你尝试添加重复的元素，集合中也不会存储重复的值。
			res := redis.Client.SAdd(ctx, key, idsStr)
			if res.Err() != nil {
				if err != nil {
					logger.WithFields(logrus.Fields{
						"err":     err,
						"actorId": actorId,
						"num":     num,
						"ids":     idsStr,
					}).Errorf("Error when locking redis ids read state")
					return nil, err
				}
			}
		}

		if len(vIds) != num {
			break
		}
		offset += num
	}
	return
}
