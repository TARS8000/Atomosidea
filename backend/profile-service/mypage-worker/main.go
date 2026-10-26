package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dbAuth    *pgxpool.Pool
	dbApp       *pgxpool.Pool
	jwtSecret   []byte
	redisClient *redis.Client
)

type Video struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type Game struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type StaticSite struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UploaderID  string    `json:"uploader_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		// ログアウト済みのトークン (blocklist:<jti>) を拒否する。Redis エラー時は拒否する (fail closed)
		jti, _ := claims["jti"].(string)
		if jti == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		blocked, err := redisClient.Exists(c.Request.Context(), "blocklist:"+jti).Result()
		if err != nil {
			log.Printf("Redis error checking blocklist: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
			return
		}
		if blocked > 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
			return
		}

		var userIDStr string
		if idStr, ok := claims["sub"].(string); ok {
			userIDStr = idStr
		} else if idStr, ok := claims["userID"].(string); ok {
			userIDStr = idStr
		} else if idStr, ok := claims["user_id"].(string); ok {
			userIDStr = idStr
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
			return
		}

		// 一覧は常に呼び出し元の ID で絞り込むため、UUID でない値は受け付けない
		parsed, err := uuid.Parse(userIDStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
			return
		}

		c.Set("userID", parsed.String())
		c.Next()
	}
}

func main() {
	var err error
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		log.Fatal("JWT_SECRET environment variable is not set")
	}

	// ログアウトのブロックリスト (blocklist:<jti>) を確認するための Redis クライアント
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}
	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	defer redisClient.Close()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		// 起動は続ける（authMiddleware が Redis エラー時に拒否する）
		log.Printf("WARNING: Failed to connect to Redis at %s: %v", redisAddr, err)
	}
	cancelPing()
	authDbUrl := os.Getenv("AUTH_DATABASE_URL")
	appDbUrl := os.Getenv("APP_DATABASE_URL")

	dbAuth, err = pgxpool.New(context.Background(), authDbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to auth database: %v\n", err)
	}
	defer dbAuth.Close()

	dbApp, err = pgxpool.New(context.Background(), appDbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to app database: %v\n", err)
	}
	defer dbApp.Close()

	r := gin.Default()
	api := r.Group("/api/my")
	api.Use(authMiddleware())
	{
		api.GET("/videos", myVideosHandler)
		api.GET("/games", myGamesHandler)
		api.GET("/static-sites", myStaticSitesHandler)
	}

	log.Println("INFO: MyPage service starting on port 8083")
	if err := r.Run(":8083"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

func myVideosHandler(c *gin.Context) {
	uploaderID := c.GetString("userID")
	rows, err := dbApp.Query(c.Request.Context(), "SELECT id, title, COALESCE(thumbnail_path, ''), uploader_id, created_at FROM videos WHERE uploader_id = $1 ORDER BY created_at DESC", uploaderID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myVideosHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve videos"})
		return
	}
	defer rows.Close()

	videos := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Title, &v.ThumbnailURL, &v.UploaderID, &v.CreatedAt); err != nil {
			log.Printf("Error scanning video row: %v", err)
			continue
		}
		videos = append(videos, v)
	}
	c.JSON(http.StatusOK, videos)
}

func myGamesHandler(c *gin.Context) {
	userID := c.GetString("userID")

	rows, err := dbApp.Query(c.Request.Context(),
		`SELECT id, title, COALESCE(thumbnail_url, ''), user_id, created_at
        FROM games
        WHERE user_id = $1
        ORDER BY created_at DESC`, userID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myGamesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing user's games"})
		return
	}
	defer rows.Close()

	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Title, &g.ThumbnailURL, &g.UploaderID, &g.CreatedAt); err != nil {
			log.Printf("Error scanning game row: %v", err)
			continue
		}
		games = append(games, g)
	}
	c.JSON(http.StatusOK, games)
}

func myStaticSitesHandler(c *gin.Context) {
	userID := c.GetString("userID")

	rows, err := dbApp.Query(c.Request.Context(),
		`SELECT id, title, COALESCE(description, ''), user_id, created_at
        FROM static_sites
        WHERE user_id = $1
        ORDER BY created_at DESC`, userID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myStaticSitesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing user's static sites"})
		return
	}
	defer rows.Close()

	sites := []StaticSite{}
	for rows.Next() {
		var s StaticSite
		if err := rows.Scan(&s.ID, &s.Title, &s.Description, &s.UploaderID, &s.CreatedAt); err != nil {
			log.Printf("Error scanning static site row: %v", err)
			continue
		}
		sites = append(sites, s)
	}
	c.JSON(http.StatusOK, sites)
}