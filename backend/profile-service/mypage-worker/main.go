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

// main で初期化し、ハンドラとミドルウェアから参照する共有の接続・設定値。
// dbApp は app-db への接続プール、jwtSecret は JWT の HS256 署名検証用の鍵、
// redisClient はログアウト済みトークンのブロックリスト確認用。
var (
	dbApp       *pgxpool.Pool
	jwtSecret   []byte
	redisClient *redis.Client
)

// Video はマイページの動画一覧の 1 件。ThumbnailURL には videos.thumbnail_path の値を入れる。
type Video struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// Game はマイページのゲーム一覧の 1 件。UploaderID には games.user_id の値を入れる。
type Game struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// StaticSite はマイページの静的サイト一覧の 1 件。UploaderID には static_sites.user_id の値を入れる。
type StaticSite struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UploaderID  string    `json:"uploader_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// authMiddleware は Authorization ヘッダーの JWT を検証し、ユーザー ID（UUID 文字列）を
// コンテキストの "userID" に入れる。検証に失敗したら 401（Redis エラー時は 500）で中断する。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. "Bearer " を取り除いたトークンを HS256 のみ許可して検証する（alg のすり替え対策）
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

		// 2. ログアウト済みのトークン (blocklist:<jti>) を拒否する。Redis エラー時は拒否する (fail closed)
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

		// 3. ユーザー ID を sub → userID → user_id の順にクレームから取り出す
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

// main は JWT 鍵・Redis・app-db を準備し、認証付きの /api/my 以下のルートをポート 8083 で公開する。
func main() {
	var err error
	// JWT の署名検証鍵がなければ起動しない
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
	// app-db（動画・ゲーム・静的サイトの各テーブル）に接続する
	appDbUrl := os.Getenv("APP_DATABASE_URL")

	dbApp, err = pgxpool.New(context.Background(), appDbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to app database: %v\n", err)
	}
	defer dbApp.Close()

	// /api/my 以下はすべて authMiddleware を通し、ログイン中のユーザー自身の投稿だけを返す
	r := gin.Default()
	api := r.Group("/api/my")
	api.Use(authMiddleware())
	{
		api.GET("/videos", myVideosHandler)
		api.GET("/games", myGamesHandler)
		api.GET("/static-sites", myStaticSitesHandler)
	}

	log.Println("INFO: MyPage service starting on port 8083")
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（本文は画像程度までなので読み取りは短め）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8083",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// myVideosHandler はログイン中のユーザーがアップロードした動画を新しい順に返す。
// 読み取れなかった行はログに残して飛ばす。
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

// myGamesHandler はログイン中のユーザーが投稿したゲームを新しい順に返す（status による絞り込みはしない）。
// 読み取れなかった行はログに残して飛ばす。
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

// myStaticSitesHandler はログイン中のユーザーが投稿した静的サイトを新しい順に返す（status による絞り込みはしない）。
// 読み取れなかった行はログに残して飛ばす。
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
