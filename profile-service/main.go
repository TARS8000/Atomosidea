package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	db          *pgxpool.Pool
	minioClient *minio.Client
	jwtSecret   []byte
	bucketName  string
)

type ProfileResponse struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	Bio                string `json:"bio"`
	IconURL            string `json:"icon_url"`
	BackgroundImageURL string `json:"background_image_url"`
	Status             string `json:"status"`
}

func main() {
	var err error
	ctx := context.Background()

	// 必須環境変数の検証
	databaseUrl := os.Getenv("DATABASE_URL")
	if databaseUrl == "" {
		log.Fatal("DATABASE_URL environment variable is not set")
	}

	jwtSecretStr := os.Getenv("JWT_SECRET")
	if jwtSecretStr == "" {
		log.Fatal("JWT_SECRET environment variable is not set")
	}
	jwtSecret = []byte(jwtSecretStr)

	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	minioAccessKey := os.Getenv("MINIO_ACCESS_KEY_ID")
	minioSecretKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	bucketName = os.Getenv("MINIO_BUCKET_NAME")

	// DB 接続の設定
	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	// MinIO クライアントの初期化
	minioClient, err = minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to MinIO: %v", err)
	}

	// MinIO バケットの自動生成処理
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		log.Fatalf("Failed to check MinIO bucket existence: %v", err)
	}
	if !exists {
		err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalf("Failed to create MinIO bucket (%s): %v", bucketName, err)
		}
		log.Printf("Successfully created MinIO bucket: %s\n", bucketName)
	}

	// バケットの読み取りポリシーを公開 (Public Read) に設定
	policy := fmt.Sprintf(`{
       "Version": "2012-10-17",
       "Statement": [
          {
             "Effect": "Allow",
             "Principal": {"AWS": ["*"]},
             "Action": ["s3:GetObject"],
             "Resource": ["arn:aws:s3:::%s/*"]
          }
       ]
    }`, bucketName)

	err = minioClient.SetBucketPolicy(ctx, bucketName, policy)
	if err != nil {
		log.Printf("Warning: Failed to set MinIO bucket policy: %v\n", err)
	} else {
		log.Printf("Successfully set public read policy for bucket: %s\n", bucketName)
	}

	// ルーティング設定
	r := gin.Default()
	api := r.Group("/api/profile")
	{
		api.GET("/me", authMiddleware(), getMyProfileHandler)
		api.GET("/status", authMiddleware(), getStatusHandler)
		api.GET("/:userId", getProfileHandler)
		api.PUT("", authMiddleware(), updateProfileHandler)
		api.PUT("/icon", authMiddleware(), updateIconHandler)
		api.PUT("/background", authMiddleware(), updateBackgroundHandler)

		// ─────────────────────────────────────────────────────────────
		// 追加: 他マイクロサービス（auth-service等）からの内部リクエスト用エンドポイント
		// ─────────────────────────────────────────────────────────────
		api.POST("/internal/create", createInternalProfileHandler)
	}

	log.Println("Profile service starting on port 8084")
	if err := r.Run(":8084"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// 内部サービス用の初期プロフィール作成ハンドラー
func createInternalProfileHandler(c *gin.Context) {
	var req struct {
		UserID   string `json:"user_id" binding:"required"`
		Username string `json:"username" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request parameters"})
		return
	}

	userIDUUID, err := uuid.Parse(req.UserID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user_id UUID format"})
		return
	}

	// ユーザーが既に存在するか確認しつつ、初期プロフィールを設定（または更新）
	// DB構造が users テーブル共有の場合と profile テーブル独立の場合どちらにも対応できるよう、更新/挿入を実行
	_, err = db.Exec(c.Request.Context(), `
		INSERT INTO users (id, username, status, updated_at)
		VALUES ($1, $2, 'active', NOW())
		ON CONFLICT (id) DO UPDATE
		SET username = EXCLUDED.username,
		    status = 'active',
		    updated_at = NOW()
		WHERE users.username IS NULL OR users.username = ''
	`, userIDUUID, req.Username)

	if err != nil {
		log.Printf("Error in internal profile creation: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize profile"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Profile initialized successfully"})
}

func getStatusHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var status string
	err := db.QueryRow(c.Request.Context(), "SELECT status FROM users WHERE id = $1", userID).Scan(&status)
	if err != nil {
		log.Printf("Error getting user status: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status})
}

func getMyProfileHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var response ProfileResponse

	// COALESCE を使って NULL の可能性のあるフィールドを安全に取得
	err := db.QueryRow(c.Request.Context(),
		"SELECT id, COALESCE(username, ''), COALESCE(bio, ''), COALESCE(icon_url, ''), COALESCE(background_image_url, ''), status FROM users WHERE id = $1",
		userID,
	).Scan(&response.ID, &response.Username, &response.Bio, &response.IconURL, &response.BackgroundImageURL, &response.Status)

	if err != nil {
		log.Printf("Error getting my profile: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, response)
}

func getProfileHandler(c *gin.Context) {
	userIdStr := c.Param("userId")
	if _, err := uuid.Parse(userIdStr); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID format (UUID required)"})
		return
	}

	var response ProfileResponse
	err := db.QueryRow(c.Request.Context(),
		"SELECT id, COALESCE(username, ''), COALESCE(bio, ''), COALESCE(icon_url, ''), COALESCE(background_image_url, ''), status FROM users WHERE id = $1",
		userIdStr,
	).Scan(&response.ID, &response.Username, &response.Bio, &response.IconURL, &response.BackgroundImageURL, &response.Status)

	if err != nil {
		log.Printf("Error getting profile for userID %s: %v", userIdStr, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, response)
}

func updateProfileHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var req struct {
		Username string `json:"username"`
		Bio      string `json:"bio"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// ユーザー名、bio を更新し、ステータスを 'active' に変更
	_, err := db.Exec(c.Request.Context(),
		"UPDATE users SET username = $1, bio = $2, status = 'active', updated_at = NOW() WHERE id = $3",
		req.Username, req.Bio, userID)
	if err != nil {
		log.Printf("Error updating profile: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Profile updated successfully"})
}

func updateIconHandler(c *gin.Context) {
	userID := c.GetString("userID")
	file, header, err := c.Request.FormFile("icon")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Icon file is required"})
		return
	}
	defer file.Close()

	objectName := fmt.Sprintf("icons/%s-%s", userID, uuid.New().String())
	_, err = minioClient.PutObject(c.Request.Context(), bucketName, objectName, file, header.Size, minio.PutObjectOptions{
		ContentType: header.Header.Get("Content-Type"),
	})
	if err != nil {
		log.Printf("Error uploading icon to MinIO: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store icon file"})
		return
	}

	iconURL := fmt.Sprintf("/user-profiles/%s", objectName)

	_, err = db.Exec(c.Request.Context(),
		"UPDATE users SET icon_url = $1, updated_at = NOW() WHERE id = $2",
		iconURL, userID)
	if err != nil {
		log.Printf("Error updating icon URL in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update icon URL"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Icon updated successfully", "icon_url": iconURL})
}

func updateBackgroundHandler(c *gin.Context) {
	userID := c.GetString("userID")
	file, header, err := c.Request.FormFile("background")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Background image file is required"})
		return
	}
	defer file.Close()

	objectName := fmt.Sprintf("backgrounds/%s-%s", userID, uuid.New().String())
	_, err = minioClient.PutObject(c.Request.Context(), bucketName, objectName, file, header.Size, minio.PutObjectOptions{
		ContentType: header.Header.Get("Content-Type"),
	})
	if err != nil {
		log.Printf("Error uploading background image to MinIO: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store background image file"})
		return
	}

	backgroundURL := fmt.Sprintf("/user-profiles/%s", objectName)

	_, err = db.Exec(c.Request.Context(),
		"UPDATE users SET background_image_url = $1, updated_at = NOW() WHERE id = $2",
		backgroundURL, userID)
	if err != nil {
		log.Printf("Error updating background image URL in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update background image URL"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Background image updated successfully", "background_image_url": backgroundURL})
}

// クレームから safe に string 型 (UUID) の User ID を抽出するヘルパー関数
func parseUserIDFromClaims(claims jwt.MapClaims) (string, bool) {
	keys := []string{"sub", "userID", "user_id", "id"}

	for _, key := range keys {
		val, exists := claims[key]
		if !exists {
			continue
		}

		if strVal, ok := val.(string); ok && strVal != "" {
			if _, err := uuid.Parse(strVal); err == nil {
				return strVal, true
			}
		}
	}
	return "", false
}

// 認証用ミドルウェア (UUID対応版)
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}

		// "Bearer <token>" の形式を安全に分解して判定
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header format must be 'Bearer {token}'"})
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			log.Printf("JWT parsing error: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
			return
		}

		userID, ok := parseUserIDFromClaims(claims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or missing UUID in token"})
			return
		}

		c.Set("userID", userID)
		c.Next()
	}
}