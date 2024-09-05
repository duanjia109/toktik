package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/juju/ratelimit"
	"net/http"
	"time"
)

// RateLimiterMiddleWare
//
//	@Description: 基于令牌桶算法的限流中间件，用于限制请求速率
//	@param fillInterval 令牌桶填充新令牌的时间间隔
//	@param cap  令牌桶的容量，即桶中最多可以存储多少令牌
//	@param quantum  每次填充时增加的令牌数量
//	@return gin.HandlerFunc
func RateLimiterMiddleWare(fillInterval time.Duration, cap, quantum int64) gin.HandlerFunc {
	//创建一个令牌桶。这个令牌桶每隔 fillInterval 时间会增加 quantum 个令牌，但总量不会超过 cap。
	bucket := ratelimit.NewBucketWithQuantum(fillInterval, cap, quantum)
	return func(c *gin.Context) {
		//尝试从令牌桶中取出 1 个令牌。如果取出的令牌数小于 1，说明桶中没有足够的令牌
		if bucket.TakeAvailable(1) < 1 {
			c.String(http.StatusForbidden, "rate limit...")
			c.Abort()
			return
		}
		c.Next()
	}
}
