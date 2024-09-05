package cached

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/redis"
	"GuGoTik/src/utils/logging"
	"context"
	"math/rand"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	redis2 "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// 表示 Redis 随机缓存的时间范围
const redisRandomScope = 1

var cacheMaps = make(map[string]*cache.Cache)

var m = new(sync.Mutex)

type cachedItem interface {
	GetID() uint32
	IsDirty() bool
}

// ScanGet 采用二级缓存(Memory-Redis)的模式读取结构体类型，并且填充到传入的结构体中，结构体需要实现IDGetter且确保ID可用。
//
// ScanGet
//
//	@Description:经过三层缓存的查找，cache - redia - db
//	@param ctx
//	@param key 本地缓存对象的标识
//	@param obj  "key + obj.id"变成真正要找的obj标识，在cache，redis中查找;从任何一级找到value都要填充你感到上一级缓存中
//	@return bool  是否存在该用户
//	@return error   错误信息
func ScanGet(ctx context.Context, key string, obj interface{}) (bool, error) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-GetFromScanCache")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("Cached.GetFromScanCache").WithContext(ctx)
	//note：这里的key代表本地缓存对象的key
	key = config.EnvCfg.RedisPrefix + key

	c := getOrCreateCache(key)
	//重点：这还怪有意思的，结构体断言成接口了，神奇
	wrappedObj := obj.(cachedItem) //note：断言成接口，确定obj要实现GetUserInfo的两个接口函数
	//note： 这里的key代表缓存对象里存的要找的数据的key
	key = key + strconv.FormatUint(uint64(wrappedObj.GetID()), 10) //cache标识 + obj.id
	if x, found := c.Get(key); found {                             //本地缓存命中
		dstVal := reflect.ValueOf(obj)       //note: reflect的用法
		dstVal.Elem().Set(x.(reflect.Value)) //note: 将命中的缓存值存到obj中
		return true, nil
	}

	//缓存没有命中，Fallback 到 Redis
	logger.WithFields(logrus.Fields{
		"key": key,
	}).Infof("Missed local memory cached")

	//获取哈希表（hash）类型键 key 对应的所有字段和值
	//Scan 方法：将 Redis 返回的结果扫描到 obj 中。obj 通常是一个指针，指向一个结构体或映射，用来接收 Redis 返回的数据。
	if err := redis.Client.HGetAll(ctx, key).Scan(obj); err != nil {
		if err != redis2.Nil {
			logger.WithFields(logrus.Fields{
				"err": err,
				"key": key,
			}).Errorf("Redis error when find struct")
			logging.SetSpanError(span, err)
			return false, err
		}
	}

	// 如果 Redis 命中，那么就存到 localCached 然后返回
	if wrappedObj.IsDirty() {
		logger.WithFields(logrus.Fields{
			"key": key,
		}).Infof("Redis hit the key")
		c.Set(key, reflect.ValueOf(obj).Elem(), cache.DefaultExpiration) //存到cache中
		return true, nil
	}

	//step：缓存没有命中，Fallback 到 DB
	logger.WithFields(logrus.Fields{
		"key": key,
	}).Warnf("Missed Redis Cached")

	//没有附加任何 Where 条件，Find 方法会返回整个表中的所有记录
	result := database.Client.WithContext(ctx).Find(obj)
	if result.RowsAffected == 0 { //q:数据库里也没有?
		logger.WithFields(logrus.Fields{
			"key": key,
		}).Warnf("Missed DB obj, seems wrong key")
		return false, result.Error
	}

	//HSet 方法：用于将一个或多个字段和值设置到哈希表类型的键中。
	//key：这是 Redis 中哈希表的键。
	//obj：这是一个包含要设置的字段和值的对象，通常是一个结构体或映射。
	//step: 数据库里找到了，把找到的数据存到redis中 key - obj，和cache中
	if result := redis.Client.HSet(ctx, key, obj); result.Err() != nil {
		logger.WithFields(logrus.Fields{
			"err": result.Err(),
			"key": key,
		}).Errorf("Redis error when set struct info")
		logging.SetSpanError(span, result.Err())
		return false, nil
	}
	c.Set(key, reflect.ValueOf(obj).Elem(), cache.DefaultExpiration)
	return true, nil
}

// ScanTagDelete 将缓存值标记为删除，下次从 cache 读取时会 FallBack 到数据库。
func ScanTagDelete(ctx context.Context, key string, obj interface{}) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-ScanTagDelete")
	defer span.End()
	logging.SetSpanWithHostname(span)
	key = config.EnvCfg.RedisPrefix + key

	redis.Client.HDel(ctx, key)

	c := getOrCreateCache(key)
	wrappedObj := obj.(cachedItem)
	key = key + strconv.FormatUint(uint64(wrappedObj.GetID()), 10)
	c.Delete(key)
}

// ScanWriteCache 写入缓存，如果 state 为 false 那么只会写入 localCached
func ScanWriteCache(ctx context.Context, key string, obj interface{}, state bool) (err error) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-ScanWriteCache")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("Cached.ScanWriteCache").WithContext(ctx)
	key = config.EnvCfg.RedisPrefix + key

	wrappedObj := obj.(cachedItem)
	key = key + strconv.FormatUint(uint64(wrappedObj.GetID()), 10)
	c := getOrCreateCache(key)
	c.Set(key, reflect.ValueOf(obj).Elem(), cache.DefaultExpiration)

	if state {
		if err = redis.Client.HGetAll(ctx, key).Scan(obj); err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
				"key": key,
			}).Errorf("Redis error when find struct info")
			logging.SetSpanError(span, err)
			return
		}
	}

	return
}

// Get 读取字符串缓存
// 从cache - redis中找，没有去数据库中找
//
//	@Description: 先从cache中找，cache没命中就到redis中找，redis如果找到把对应的key-value存到cache中
//	@param ctx  ？用来干嘛
//	@param key  要查找的key
//	@return string  查找到的key的value，""代表没找到
//	@return bool  是否找到，异常也返回false
//	@return error  相关错误
func Get(ctx context.Context, key string) (string, bool, error) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-GetFromStringCache")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("Cached.GetFromStringCache").WithContext(ctx)
	//REDIS_PREFIX=GuGoTik
	key = config.EnvCfg.RedisPrefix + key

	//q 为什么从cacheMaps获取cache？  key为strings
	//c代表cache对象
	c := getOrCreateCache("strings")
	//在cache中查找key
	if x, found := c.Get(key); found {
		return x.(string), true, nil //找到了，返回结果
	}

	//缓存没有命中，Fallback 到 Redis
	logger.WithFields(logrus.Fields{
		"key": key,
	}).Infof("Missed local memory cached")

	var result *redis2.StringCmd
	//result.Err() != nil  检查获取redis数据的操作是否错误
	//redis2.Nil 表示没有找到指定键的值
	//result.Err() 不为 nil 且不是 redis2.Nil 表示在获取 Redis 数据时发生了其他类型的错误
	if result = redis.Client.Get(ctx, key); result.Err() != nil && result.Err() != redis2.Nil {
		//出错了
		logger.WithFields(logrus.Fields{
			"err":    result.Err(),
			"string": key,
		}).Errorf("Redis error when find string")
		logging.SetSpanError(span, result.Err())
		return "", false, nil //在redis中查找遇到错误，返回false
	}

	value, err := result.Result()

	switch {
	case err == redis2.Nil: //表示在 Redis 中没有找到指定的键
		return "", false, nil
	case err != nil: //表示发生了其他类型的错误，需要进行错误处理
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Err when write Redis")
		logging.SetSpanError(span, err)
		return "", false, err
	default:
		//成功从redis中获取到值value
		//将获取到的 key  value设置到内存缓存中，以便下次快速获取
		c.Set(key, value, cache.DefaultExpiration)
		return value, true, nil
	}
}

// GetWithFunc  从缓存中获取字符串，如果不存在调用 Func 函数获取
//
//	@Description:先从cache - redis缓存结构中获取key的value值，
//	如果存在：直接返回
//	如果不存在：调用f获取value，获取成功之后再把key-value写入到cache和redis中
//
//	@param ctx
//	@param key 要找的key
//	@param f  获取value的函数
//	@return string key对应的value
//	@return error
func GetWithFunc(ctx context.Context, key string, f func(ctx context.Context, key string) (string, error)) (string, error) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-GetFromStringCacheWithFunc")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//在 cache - redis 缓存结构中查找key对应的value
	value, ok, err := Get(ctx, key)

	if err != nil {
		return "", err
	}

	//ok标志是否读到
	if ok { //都到了
		return value, nil
	}

	// 如果不存在，那么调用f获取一个value
	value, err = f(ctx, key)

	if err != nil {
		return "", err
	}

	Write(ctx, key, value, true) //把key - value存到cache - redis中
	return value, nil
}

// Write 写入字符串缓存cache中，如果 state 为 false 则只写入 Local Memory
//
//	@Description:给key加上前缀config.EnvCfg.RedisPrefix，之后将key value写入cache中，如果state为true则也写入到redis中
//	@param ctx
//	@param key  要写入缓存的key
//	@param value  要写入缓存的value
//	@param state   true表示同时写入redis
func Write(ctx context.Context, key string, value string, state bool) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-SetStringCache")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//注意：这里要加一个前缀
	key = config.EnvCfg.RedisPrefix + key

	c := getOrCreateCache("strings")
	c.Set(key, value, cache.DefaultExpiration)

	if state {
		redis.Client.Set(ctx, key, value, 120*time.Hour+time.Duration(rand.Intn(redisRandomScope))*time.Second)
	}
}

// TagDelete
// 删除字符串缓存，从cache和redis中都删除键为key的缓存
//
//	@Description:
//	@param ctx
//	@param key   要删除的缓存key
func TagDelete(ctx context.Context, key string) {
	ctx, span := tracing.Tracer.Start(ctx, "Cached-DeleteStringCache")
	defer span.End()
	logging.SetSpanWithHostname(span)
	key = config.EnvCfg.RedisPrefix + key

	c := getOrCreateCache("strings")
	c.Delete(key)

	redis.Client.Del(ctx, key)
}

// getOrCreateCache
//
//	@Description:获取或创建一个缓存对象。它通过缓存名称（name）来查找是否已经存在对应的缓存对象，
//	如果不存在，则创建一个新的缓存对象，并将其存储在全局缓存映射（cacheMaps）中。
//	@param name  cache的名字
//	@return *cache.Cache
func getOrCreateCache(name string) *cache.Cache {
	cc, ok := cacheMaps[name]
	if !ok {
		m.Lock()
		defer m.Unlock()
		//lable: 这里需要重新获取一下缓存  避免在274返回之后 - 276加锁成功之前有人创建了缓存
		cc, ok := cacheMaps[name]
		if !ok {
			//创建一个具有默认过期时间为 5 分钟，清理间隔为 10 分钟的缓存对象
			cc = cache.New(5*time.Minute, 10*time.Minute)
			cacheMaps[name] = cc
			return cc
		}
		return cc
	}
	return cc
}

// CacheAndRedisGet 从内存缓存和 Redis 缓存中读取数据
func CacheAndRedisGet(ctx context.Context, key string, obj interface{}) (bool, error) {
	ctx, span := tracing.Tracer.Start(ctx, "CacheAndRedisGet")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("CacheAndRedisGet").WithContext(ctx)
	key = config.EnvCfg.RedisPrefix + key

	c := getOrCreateCache(key)
	wrappedObj := obj.(cachedItem)
	key = key + strconv.FormatUint(uint64(wrappedObj.GetID()), 10)
	if x, found := c.Get(key); found {
		dstVal := reflect.ValueOf(obj)
		dstVal.Elem().Set(x.(reflect.Value))
		return true, nil
	}

	// 缓存没有命中，Fallback 到 Redis
	logger.WithFields(logrus.Fields{
		"key": key,
	}).Infof("Missed local memory cached")

	if err := redis.Client.HGetAll(ctx, key).Scan(obj); err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
			"key": key,
		}).Errorf("Redis error when find struct")
		logging.SetSpanError(span, err)
		return false, err
	}

	// 如果 Redis 命中，那么就存到 localCached 然后返回
	if wrappedObj.IsDirty() {
		logger.WithFields(logrus.Fields{
			"key": key,
		}).Infof("Redis hit the key")
		c.Set(key, reflect.ValueOf(obj).Elem(), cache.DefaultExpiration)
		return true, nil
	}

	logger.WithFields(logrus.Fields{
		"key": key,
	}).Warnf("Missed Redis Cached")

	return false, nil
}

// ActionRedisSync
// 定期进行任务f
// 根据名字，该函数可能的作用是：定期执行指定的同步任务，这个同步任务涉及与 Redis 的交互
// 可能的使用场景
// 缓存同步：定期将某些数据从应用程序同步到 Redis 缓存。
// 持久化数据：定期从 Redis 读取数据并持久化到其他存储系统中。
// 定期清理：定期清理 Redis 中的过期数据或其他不必要的数据。
// 统计信息：定期从 Redis 中收集统计信息并进行分析或存储。
//
//	@Description:
//	@param time  间隔时间
//	@param f   动作
//
// q:这个函数为什么没有被调用？
func ActionRedisSync(time time.Duration, f func(client redis2.UniversalClient) error) {
	go func() {
		daemon := NewTick(time, f)
		daemon.Start()
	}()
}
