package main

// imagelimit.go：画像の差し替えをユーザーごとに制限する（DoS 対策）。
// game-upload-api / static-site-upload-api の guard.go と同じ Redis のキーを使うので、
// アイコン・背景・サムネイルの変更を合わせて 1 時間あたり IMAGE_UPLOAD_LIMIT_PER_HOUR 回（既定 30）までになる。

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/atmosidea/shared/queue"
	"github.com/atmosidea/shared/ratelimit"
	"github.com/gin-gonic/gin"
)

// imageLimitMiddleware は authMiddleware の後に置く。管理者と、画像を含まない小さな更新は数えない。
// Redis に届かないときはログを出して通す。
func imageLimitMiddleware() gin.HandlerFunc {
	limit := ratelimit.EnvInt("IMAGE_UPLOAD_LIMIT_PER_HOUR", 30)
	return func(c *gin.Context) {
		if ratelimit.TokenBool(c.GetHeader("Authorization"), "isAdmin") {
			c.Next()
			return
		}
		if c.Request.ContentLength >= 0 && c.Request.ContentLength < 64<<10 {
			c.Next()
			return
		}
		d, err := ratelimit.CheckAction(c.Request.Context(), queue.RedisClient, c.GetString("userID"), "image", limit, time.Hour, "画像の変更")
		if err != nil {
			log.Printf("WARN: image rate limit check failed, allowing: %v", err)
		}
		if !d.Allowed {
			c.Header("Retry-After", strconv.Itoa(int(d.RetryAfter/time.Second)+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": d.Reason})
			return
		}
		c.Next()
	}
}
