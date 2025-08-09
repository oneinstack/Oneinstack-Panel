package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 速率限制器
type RateLimiter struct {
	visitors map[string]*Visitor
	mutex    sync.RWMutex
	rate     int           // 每分钟允许的请求数
	window   time.Duration // 时间窗口
}

// Visitor 访问者信息
type Visitor struct {
	requests []time.Time
	mutex    sync.Mutex
}

// NewRateLimiter 创建新的速率限制器
func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*Visitor),
		rate:     rate,
		window:   window,
	}

	// 启动清理协程
	go rl.cleanup()

	return rl
}

// IsAllowed 检查是否允许请求
func (rl *RateLimiter) IsAllowed(ip string) bool {
	rl.mutex.RLock()
	visitor, exists := rl.visitors[ip]
	rl.mutex.RUnlock()

	if !exists {
		rl.mutex.Lock()
		visitor = &Visitor{
			requests: make([]time.Time, 0),
		}
		rl.visitors[ip] = visitor
		rl.mutex.Unlock()
	}

	visitor.mutex.Lock()
	defer visitor.mutex.Unlock()

	now := time.Now()

	// 清理过期的请求记录
	cutoff := now.Add(-rl.window)
	validRequests := make([]time.Time, 0)
	for _, reqTime := range visitor.requests {
		if reqTime.After(cutoff) {
			validRequests = append(validRequests, reqTime)
		}
	}
	visitor.requests = validRequests

	// 检查是否超过限制
	if len(visitor.requests) >= rl.rate {
		return false
	}

	// 添加当前请求
	visitor.requests = append(visitor.requests, now)
	return true
}

// cleanup 定期清理过期的访问者记录
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mutex.Lock()
			now := time.Now()
			cutoff := now.Add(-rl.window * 2) // 保留两个窗口的数据

			for ip, visitor := range rl.visitors {
				visitor.mutex.Lock()
				if len(visitor.requests) == 0 || visitor.requests[len(visitor.requests)-1].Before(cutoff) {
					delete(rl.visitors, ip)
				}
				visitor.mutex.Unlock()
			}
			rl.mutex.Unlock()
		}
	}
}

// RateLimitMiddleware 速率限制中间件
func RateLimitMiddleware(rate int, window time.Duration) gin.HandlerFunc {
	limiter := NewRateLimiter(rate, window)

	return func(c *gin.Context) {
		ip := c.ClientIP()

		if !limiter.IsAllowed(ip) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Rate limit exceeded",
				"code":        "RATE_LIMIT_EXCEEDED",
				"retry_after": int(window.Seconds()),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// LoginRateLimitMiddleware 登录专用的速率限制中间件
func LoginRateLimitMiddleware() gin.HandlerFunc {
	// 登录限制：每分钟最多5次尝试
	return RateLimitMiddleware(5, time.Minute)
}

// APIRateLimitMiddleware API通用速率限制中间件
func APIRateLimitMiddleware() gin.HandlerFunc {
	// API限制：每分钟最多100次请求
	return RateLimitMiddleware(100, time.Minute)
}
