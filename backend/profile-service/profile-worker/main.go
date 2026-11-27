package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

// プロセス全体で共有する接続・設定値。main で環境変数から初期化し、ハンドラーとワーカーから参照する。
var (
	// db は profile-db（users テーブル）への接続プール。
	db *pgxpool.Pool
	// minioClient はプロフィール画像を公開配信するバケット（MINIO_BUCKET_NAME）用のクライアント。
	minioClient *minio.Client
	// jwtSecret は HS256 の JWT 署名検証に使う共有鍵。
	jwtSecret []byte
	// bucketName はプロフィール画像の保存先バケット名。
	bucketName string
	// sfspApiUrl / sfspApiKey はファイルスキャン基盤 sfsp-api の URL と API キー。
	sfspApiUrl string
	sfspApiKey string
	// internalApiKey は /internal 配下の API を呼ぶための共有鍵。
	internalApiKey string
	// maxImageUploadBytes は画像 1 ファイルの上限サイズ（既定 10MiB、MAX_IMAGE_UPLOAD_BYTES で上書き可）。
	maxImageUploadBytes int64 = 10 << 20
	// sfspCleanMinioClient はスキャン済み（clean）ファイルを置く SFSP 側 MinIO のクライアント。
	sfspCleanMinioClient *minio.Client
	// sfspCleanBucketName は SFSP 側でスキャン済みファイルが置かれるバケット名。
	sfspCleanBucketName = "clean-files"
	// sfspHTTPClient は sfsp-api への送信用。ファイルをストリーミング送信するためタイムアウトを長めにしている。
	sfspHTTPClient = &http.Client{Timeout: 10 * time.Minute}
)

// multipartOverheadBytes は multipart の境界やヘッダー分として、リクエスト全体の上限に上乗せする量。
const multipartOverheadBytes = 1 << 20

// errFileTooLarge はファイルパートが MAX_IMAGE_UPLOAD_BYTES を超えたことを示す。
var errFileTooLarge = errors.New("file exceeds the upload size limit")

// SfspUploadResponse は SFSP のアップロード応答のうち、このサービスが使うフィールドだけを持つ。
type SfspUploadResponse struct {
	JobID string `json:"job_id"`
}

// ProfileResponse はプロフィール取得 API の応答。NULL のカラムは空文字列で返す。
// IconSFSPJobID / BackgroundSFSPJobID が空でなければ、その画像はスキャン待ちである。
type ProfileResponse struct {
	ID                  string `json:"id"`
	Username            string `json:"username"`
	Bio                 string `json:"bio"`
	IconURL             string `json:"icon_url"`
	BackgroundImageURL  string `json:"background_image_url"`
	IconSFSPJobID       string `json:"icon_sfsp_job_id"`
	BackgroundSFSPJobID string `json:"background_sfsp_job_id"`
	Status              string `json:"status"`
}

// imageExtensions は保存を許可する画像の Content-Type と拡張子の対応。
// HTML/SVG 等を「サムネイル」として保存させないため、これ以外は拒否する。
var imageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// main は環境変数・DB・MinIO・Redis を初期化し、スキャン完了イベントを処理するワーカーを起動したうえで、
// プロフィール API の HTTP サーバーをポート 8084 で起動する。
func main() {
	var err error
	ctx := context.Background()

	// 1. 必須環境変数の検証
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

	sfspApiUrl = os.Getenv("SFSP_API_URL")
	if sfspApiUrl == "" {
		log.Fatal("SFSP_API_URL environment variable is not set")
	}
	sfspApiKey = os.Getenv("SFSP_API_KEY")
	if sfspApiKey == "" {
		log.Println("Warning: SFSP_API_KEY is not set; requests to sfsp-api will be rejected")
	}

	// 内部 API（auth-service からの呼び出し）用の共有鍵。空なら内部 API はすべて拒否する
	internalApiKey = os.Getenv("INTERNAL_API_KEY")
	if internalApiKey == "" {
		log.Println("Warning: INTERNAL_API_KEY is not set; all internal endpoints will reject requests")
	}

	// 画像サイズ上限の上書き。不正な値なら起動を止める
	if v := os.Getenv("MAX_IMAGE_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			log.Fatalf("Invalid MAX_IMAGE_UPLOAD_BYTES: %q", v)
		}
		maxImageUploadBytes = n
	}

	// スキャン済みファイルを取得する SFSP 側 MinIO の接続情報
	sfspCleanMinioEndpoint := os.Getenv("SFSP_CLEAN_MINIO_ENDPOINT")
	sfspCleanMinioAccessKey := os.Getenv("SFSP_CLEAN_MINIO_ACCESS_KEY_ID")
	sfspCleanMinioSecretKey := os.Getenv("SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY")
	sfspCleanMinioUseSSL := os.Getenv("SFSP_CLEAN_MINIO_USE_SSL") == "true"

	// 2. DB 接続の設定
	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	// 3. MinIO クライアントの初期化（公開用と SFSP の clean バケット用の 2 つ）
	minioClient, err = minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to MinIO: %v", err)
	}

	sfspCleanMinioClient, err = minio.New(sfspCleanMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspCleanMinioAccessKey, sfspCleanMinioSecretKey, ""),
		Secure: sfspCleanMinioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to SFSP MinIO: %v", err)
	}

	// 4. MinIO バケットの自動生成処理
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
	// プロフィール画像は "/<bucket>/<key>" の URL で直接配信するため、GetObject のみ匿名で許可する。失敗しても起動は続ける
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

	// 5. ロガー・共通設定・Redis（キューとトークンのブロックリストで使う）の初期化
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	zapSugar := zapLogger.Sugar()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if err := queue.ConnectRedis(cfg, zapSugar); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// 6. スキャン完了イベントを処理するワーカーをバックグラウンドで起動
	go profileWorker(ctx)

	// 7. ルーティング設定
	r := gin.Default()

	// 内部 API（Nginx からは到達させない。X-Internal-Key 必須）
	internal := r.Group("/internal", internalAuthMiddleware())
	{
		internal.POST("/create", createInternalProfileHandler)
		internal.DELETE("/users/:userId", deleteInternalUserHandler)
	}

	// 公開 API。他人のプロフィール取得（GET /:userId）以外は JWT 認証が必要
	api := r.Group("/api/profile")
	{
		api.GET("/me", authMiddleware(), getMyProfileHandler)
		api.GET("/status", authMiddleware(), getStatusHandler)
		api.GET("/:userId", getProfileHandler)
		api.PUT("", authMiddleware(), updateProfileHandler)
		api.PUT("/icon", authMiddleware(), updateIconHandler)
		api.PUT("/background", authMiddleware(), updateBackgroundHandler)
	}

	log.Println("Profile service starting on port 8084")
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（本文は画像程度までなので読み取りは短め）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8084",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// internalAuthMiddleware は X-Internal-Key を INTERNAL_API_KEY と定数時間で比較する。
// INTERNAL_API_KEY が空の場合はすべて拒否する。
func internalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := c.GetHeader("X-Internal-Key")
		if internalApiKey == "" || provided == "" ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(internalApiKey)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		c.Next()
	}
}

// createInternalProfileHandler は内部サービス（auth-service）から呼ばれ、user_id と username で初期プロフィールを作成する。
// 既にユーザー名が設定済みの行は上書きしない。
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

	// 行が無ければ作成し、既存の行はユーザー名が未設定のときだけ更新する
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

// deleteInternalUserHandler はアカウント削除時に auth-service から呼ばれ、
// profile-db の行と user-profiles のアイコン・背景画像を削除する。冪等で、成功時は 204。
func deleteInternalUserHandler(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID format (UUID required)"})
		return
	}
	ctx := c.Request.Context()
	uid := userID.String()

	// 1. 削除対象のオブジェクトを集める
	// 削除対象のオブジェクト: DB に記録された URL と、icons/<id>- / backgrounds/<id>- で始まるもの
	// （map を集合として使い、重複を除く）
	objects := map[string]struct{}{}
	var iconURL, bgURL string
	err = db.QueryRow(ctx,
		"SELECT COALESCE(icon_url, ''), COALESCE(background_image_url, '') FROM users WHERE id = $1",
		userID).Scan(&iconURL, &bgURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("Failed to look up profile %s for deletion: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile"})
		return
	}
	for _, u := range []string{iconURL, bgURL} {
		if key, ok := objectKeyFromURL(u); ok {
			objects[key] = struct{}{}
		}
	}
	// DB に記録されていない過去の画像も残さないよう、ユーザー ID のプレフィックスで一覧して追加する
	for _, prefix := range []string{"icons/" + uid + "-", "backgrounds/" + uid + "-"} {
		for obj := range minioClient.ListObjects(ctx, bucketName, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				log.Printf("Failed to list profile objects for %s: %v", uid, obj.Err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile images"})
				return
			}
			objects[obj.Key] = struct{}{}
		}
	}

	// 2. 画像を削除する。既に無いオブジェクトは成功扱いにして冪等にする
	for key := range objects {
		if err := minioClient.RemoveObject(ctx, bucketName, key, minio.RemoveObjectOptions{}); err != nil {
			if minio.ToErrorResponse(err).Code == "NoSuchKey" {
				continue
			}
			log.Printf("Failed to remove profile object %s for %s: %v", key, uid, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile images"})
			return
		}
	}

	// 3. 画像の削除がすべて成功してから DB の行を削除する（途中で失敗しても再呼び出しで残りを消せるようにするため）
	if _, err := db.Exec(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
		log.Printf("Failed to delete profile row %s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile"})
		return
	}

	log.Printf("Deleted profile %s (%d objects)", uid, len(objects))
	c.Status(http.StatusNoContent)
}

// objectKeyFromURL は "/user-profiles/<key>" 形式の URL からオブジェクトキーを取り出す。
// 実際には "/<bucketName>/" で始まるかで判定し、このバケット以外を指す URL や空のキーは false を返す。
func objectKeyFromURL(u string) (string, bool) {
	prefix := "/" + bucketName + "/"
	if u == "" || !strings.HasPrefix(u, prefix) {
		return "", false
	}
	key := strings.TrimPrefix(u, prefix)
	if key == "" {
		return "", false
	}
	return key, true
}

// getStatusHandler は認証済みユーザーのプロフィールの status（'active' / 'pending' など）を返す。
// 行が無い場合を含め、取得に失敗したときは 404 を返す。
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

// getMyProfileHandler は認証済みユーザー自身のプロフィールを返す。行が無ければ先に作成する。
func getMyProfileHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var response ProfileResponse

	// 認証済みユーザーにプロフィール行が無い場合（初期管理者、auth-service からの非同期作成の失敗等）は
	// 404 にせず、その場で初期プロフィールを作成する。ユーザー名が無ければ初回設定画面へ誘導するため 'pending' にする
	username := c.GetString("username")
	status := "active"
	if username == "" {
		status = "pending"
	}
	if _, err := db.Exec(c.Request.Context(),
		"INSERT INTO users (id, username, status, updated_at) VALUES ($1, NULLIF($2, ''), $3, NOW()) ON CONFLICT (id) DO NOTHING",
		userID, username, status); err != nil {
		log.Printf("Error ensuring profile for user %s: %v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load profile"})
		return
	}

	// NULL のカラムは空文字列に変換して取得する
	err := db.QueryRow(c.Request.Context(),
		"SELECT id, COALESCE(username, ''), COALESCE(bio, ''), COALESCE(icon_url, ''), COALESCE(background_image_url, ''), COALESCE(icon_sfsp_job_id::text, ''), COALESCE(background_sfsp_job_id::text, ''), status FROM users WHERE id = $1",
		userID,
	).Scan(&response.ID, &response.Username, &response.Bio, &response.IconURL, &response.BackgroundImageURL, &response.IconSFSPJobID, &response.BackgroundSFSPJobID, &response.Status)

	if err != nil {
		log.Printf("Error getting my profile: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, response)
}

// getProfileHandler は任意のユーザーのプロフィールを返す（認証不要）。userId は UUID 形式のみ受け付ける。
func getProfileHandler(c *gin.Context) {
	userIdStr := c.Param("userId")
	if _, err := uuid.Parse(userIdStr); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID format (UUID required)"})
		return
	}

	var response ProfileResponse
	err := db.QueryRow(c.Request.Context(),
		"SELECT id, COALESCE(username, ''), COALESCE(bio, ''), COALESCE(icon_url, ''), COALESCE(background_image_url, ''), COALESCE(icon_sfsp_job_id::text, ''), COALESCE(background_sfsp_job_id::text, ''), status FROM users WHERE id = $1",
		userIdStr,
	).Scan(&response.ID, &response.Username, &response.Bio, &response.IconURL, &response.BackgroundImageURL, &response.IconSFSPJobID, &response.BackgroundSFSPJobID, &response.Status)

	if err != nil {
		log.Printf("Error getting profile for userID %s: %v", userIdStr, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, response)
}

// updateProfileHandler は認証済みユーザーのユーザー名と自己紹介（bio）を更新する。
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

// updateIconHandler はアイコン画像のアップロードを受け付け、スキャンに回す。
func updateIconHandler(c *gin.Context) {
	handleProfileImageUpload(c, "icon", "icon_sfsp_job_id", "Icon")
}

// updateBackgroundHandler は背景画像のアップロードを受け付け、スキャンに回す。
func updateBackgroundHandler(c *gin.Context) {
	handleProfileImageUpload(c, "background", "background_sfsp_job_id", "Background")
}

// handleProfileImageUpload は multipart の fieldName のファイルを画像形式・サイズを確認しながら
// sfsp-api にストリーミングで転送し、jobColumn にジョブ ID を保存する。
// jobColumn は呼び出し元の固定値のみ（ユーザー入力を SQL に入れない）。
func handleProfileImageUpload(c *gin.Context, fieldName, jobColumn, label string) {
	userID := c.GetString("userID")

	// 1. リクエスト全体のサイズを制限する
	// Content-Length で明らかに大きいものは先に弾き、申告が無い・偽りの場合に備えて本文も MaxBytesReader で制限する
	if c.Request.ContentLength > maxImageUploadBytes+multipartOverheadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File is too large"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageUploadBytes+multipartOverheadBytes)

	mr, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "multipart/form-data is required"})
		return
	}

	// 2. 対象のファイルパートを探す
	// ParseMultipartForm を使わず MultipartReader で順に読むことで、ファイル全体をメモリや一時ファイルに展開しない
	var part *multipart.Part
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isTooLarge(err) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File is too large"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart body"})
			return
		}
		if p.FormName() == fieldName && p.FileName() != "" {
			part = p
			break
		}
		// 対象外のパートは読み捨てる
		if _, err := io.Copy(io.Discard, p); err != nil {
			if isTooLarge(err) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File is too large"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart body"})
			return
		}
	}
	if part == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": label + " file is required"})
		return
	}
	defer part.Close()

	// 3. 先頭 512 バイトで画像形式を判定する（スキャン後にワーカーでも再確認する）
	// 512 バイトは http.DetectContentType が参照する最大長
	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		if isTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File is too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read upload file"})
		return
	}
	head = head[:n]
	if n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": label + " file is empty"})
		return
	}
	ext, ok := imageExtensions[http.DetectContentType(head)]
	if !ok {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Only JPEG, PNG, GIF and WebP images are allowed"})
		return
	}

	// 4. 読み済みの先頭部分と残りを連結し、ファイル単体の上限を掛けて sfsp-api に転送する
	// ファイル名はクライアント指定のものを使わず、フィールド名と判定した拡張子から組み立てる
	src := &limitedReader{r: io.MultiReader(bytes.NewReader(head), part), remaining: maxImageUploadBytes}

	var sfspResp SfspUploadResponse
	if err := uploadToSFSP(c.Request.Context(), src, fieldName+ext, "profile", &sfspResp); err != nil {
		if isTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File is too large"})
			return
		}
		log.Printf("Error forwarding %s to SFSP: %v", fieldName, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File scanning service is unavailable or request timed out"})
		return
	}

	// 5. 返ってきたジョブ ID を保存する。スキャン完了後、ワーカーはこの ID でユーザーを特定する
	// （新しいアップロードで上書きされた場合、古いジョブの結果は反映されない）
	sfspJobID, err := uuid.Parse(sfspResp.JobID)
	if err != nil {
		log.Printf("Invalid Job ID from SFSP: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid Job ID from SFSP"})
		return
	}

	_, err = db.Exec(c.Request.Context(),
		"UPDATE users SET "+jobColumn+" = $1, updated_at = NOW() WHERE id = $2",
		sfspJobID, userID)
	if err != nil {
		log.Printf("Error updating %s: %v", jobColumn, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": label + " upload accepted for scanning",
		"job_id":  sfspJobID.String(),
		"status":  "scanning",
	})
}

// limitedReader は remaining バイトを超えて読もうとすると errFileTooLarge を返す。
// io.LimitReader と違い、上限ちょうどで黙って打ち切らず、超過をエラーとして呼び出し元に知らせる。
type limitedReader struct {
	r         io.Reader
	remaining int64
}

// Read は最大 remaining+1 バイトまで読み、1 バイトでも上限を超えた時点で errFileTooLarge を返す。
// 上限ちょうどのファイルと超過したファイルを区別するため、1 バイト余分に読ませている。
func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining < 0 {
		return 0, errFileTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		return n, errFileTooLarge
	}
	return n, err
}

// isTooLarge は err がサイズ超過（limitedReader の errFileTooLarge か、MaxBytesReader の MaxBytesError）かを判定する。
func isTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	return errors.Is(err, errFileTooLarge) || errors.As(err, &mbe)
}

// uploadToSFSP は src を multipart で sfsp-api にストリーミング送信する（全体をメモリに載せない）。
// targetService は完了イベントの配送先キュー（"profile" なら sfsp:completed:profile）を決める値で、応答の job_id を resp に格納する。
func uploadToSFSP(ctx context.Context, src io.Reader, filename, targetService string, resp *SfspUploadResponse) error {
	// io.Pipe の書き込み側で multipart 本文を生成し、読み込み側をそのままリクエスト本文にする
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	// 書き込み側のエラー（サイズ超過など）を呼び出し元に返すために保持する
	writeErrCh := make(chan error, 1)
	go func() {
		err := func() error {
			if err := writer.WriteField("target_service", targetService); err != nil {
				return fmt.Errorf("failed to write target_service: %w", err)
			}
			part, err := writer.CreateFormFile("file", filename)
			if err != nil {
				return fmt.Errorf("failed to create form: %w", err)
			}
			if _, err := io.Copy(part, src); err != nil {
				return fmt.Errorf("failed to copy file: %w", err)
			}
			return writer.Close()
		}()
		writeErrCh <- err
		pw.CloseWithError(err)
	}()

	sfspRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, sfspApiUrl+"/api/v1/files", pr)
	if err != nil {
		// 読み手がいないと書き込み goroutine が止まったままになるため、パイプを閉じて終了を待つ
		pr.CloseWithError(err)
		<-writeErrCh
		return fmt.Errorf("failed to create SFSP request: %w", err)
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())
	sfspRequest.Header.Set("X-SFSP-API-Key", sfspApiKey)

	sfspResponse, doErr := sfspHTTPClient.Do(sfspRequest)
	// sfsp-api が途中で応答した場合でも書き込み goroutine を終わらせる
	pr.CloseWithError(errors.New("request finished"))
	writeErr := <-writeErrCh
	// サイズ超過は送信エラーより優先して返す（呼び出し元で 413 にするため）
	if isTooLarge(writeErr) {
		if sfspResponse != nil {
			sfspResponse.Body.Close()
		}
		return writeErr
	}
	if doErr != nil {
		return fmt.Errorf("SFSP request failed: %w", doErr)
	}
	defer sfspResponse.Body.Close()

	// 応答本文は 1MiB までに制限して読む
	respBytes, err := io.ReadAll(io.LimitReader(sfspResponse.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read SFSP response: %w", err)
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		return fmt.Errorf("SFSP returned non-2xx status %d: %s", sfspResponse.StatusCode, string(respBytes))
	}
	// 2xx でも本文を送り切れていなければ失敗とする
	if writeErr != nil {
		return fmt.Errorf("failed to send file to SFSP: %w", writeErr)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return fmt.Errorf("failed to decode SFSP response: %w", err)
	}

	return nil
}

// profileWorker は sfsp:completed:profile を DequeueReliable で取り出し、処理が終わってから Ack する。
// 起動時に、前回の実行で Ack されなかったイベントをキューに戻す。
func profileWorker(ctx context.Context) {
	profileCompletionQueue := queue.ProfileCompletionQueue

	if n, err := queue.RecoverProcessing(ctx, profileCompletionQueue); err != nil {
		log.Printf("Failed to recover unacknowledged profile events: %v", err)
	} else if n > 0 {
		log.Printf("Recovered %d unacknowledged profile events", n)
	}

	log.Println("Profile Worker started. Waiting for scan completion events...")
	for {
		// タイムアウト 0 でイベントが来るまでブロックする。取り出したイベントは処理中リストに移される
		payload, err := queue.DequeueReliable(ctx, profileCompletionQueue, 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error fetching event from Redis: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		processProfileEventWithRetry(ctx, payload)

		// 成功・失敗にかかわらず処理を終えたイベントは Ack し、処理中リストから取り除く（失敗したイベントも再処理しない）
		if err := queue.Ack(ctx, profileCompletionQueue, payload); err != nil {
			log.Printf("Failed to ack profile event: %v", err)
		}
	}
}

// errTransient は再試行で回復しうる失敗（DB・MinIO の一時的なエラー）を示す。
// errTransient で包まれていないエラーは恒久的な失敗（画像形式不正・サイズ超過など）として扱い、再試行しない。
type errTransient struct{ err error }

// Error は内側のエラーのメッセージを返す。
func (e errTransient) Error() string { return e.err.Error() }

// Unwrap は errors.Is / errors.As のために内側のエラーを返す。
func (e errTransient) Unwrap() error { return e.err }

// transient は fmt.Errorf と同じ書式でエラーを作り、一時的な失敗として errTransient で包む。
func transient(format string, args ...any) error {
	return errTransient{fmt.Errorf(format, args...)}
}

// processProfileEventWithRetry は一時的な失敗なら数回再試行し、それでも失敗したら
// ジョブ ID をクリアして終端状態にする（フロントエンドのポーリングを終わらせるため）。
func processProfileEventWithRetry(ctx context.Context, payload string) {
	// 解析できないイベントは再試行しても直らないため、ログだけ残して捨てる
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		log.Printf("ERROR: Unmarshalling event payload: %v", err)
		return
	}

	// 最大 3 回試行する。待ち時間は 2 秒、4 秒と試行ごとに延ばす
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		err := processProfileEvent(ctx, ev)
		if err == nil {
			return
		}
		var te errTransient
		if !errors.As(err, &te) {
			log.Printf("Profile event for job %s failed: %v", ev.JobID, err)
			clearProfileJob(ctx, ev.JobID)
			return
		}
		if attempt >= maxAttempts {
			log.Printf("Profile event for job %s failed after %d attempts: %v", ev.JobID, attempt, err)
			clearProfileJob(ctx, ev.JobID)
			return
		}
		log.Printf("Profile event for job %s failed (attempt %d): %v. Retrying...", ev.JobID, attempt, err)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
}

// clearProfileJob はジョブ ID が残っていれば NULL にする（新しいジョブで上書きされていれば何もしない）。
func clearProfileJob(ctx context.Context, jobID uuid.UUID) {
	if _, err := db.Exec(ctx, `
		UPDATE users SET
		    icon_sfsp_job_id = CASE WHEN icon_sfsp_job_id = $1 THEN NULL ELSE icon_sfsp_job_id END,
		    background_sfsp_job_id = CASE WHEN background_sfsp_job_id = $1 THEN NULL ELSE background_sfsp_job_id END
		WHERE icon_sfsp_job_id = $1 OR background_sfsp_job_id = $1`, jobID); err != nil {
		log.Printf("Failed to clear scan job %s: %v", jobID, err)
	}
}

// processProfileEvent は 1 件の完了イベントを処理する。defer はこの関数の終了時に実行されるため、
// ループ内でファイルディスクリプタや一時ファイルが溜まらない。
// clean なら SFSP 側からファイルを取得して再検証し、公開バケットへ保存してプロフィールの URL を差し替える。
// 再試行すべき失敗は errTransient、それ以外の失敗は通常のエラーで返す。
func processProfileEvent(ctx context.Context, ev event.ScanCompletionEvent) error {
	// 1. ジョブ ID からユーザーと、アイコン・背景のどちらのジョブかを特定する
	var userID string
	var isIcon bool
	err := db.QueryRow(ctx,
		"SELECT id::text, COALESCE(icon_sfsp_job_id = $1, false) FROM users WHERE icon_sfsp_job_id = $1 OR background_sfsp_job_id = $1 LIMIT 1",
		ev.JobID).Scan(&userID, &isIcon)
	if errors.Is(err, pgx.ErrNoRows) {
		// 新しいアップロードで置き換えられた、またはアカウントが削除された
		log.Printf("No user found for job %s; discarding event", ev.JobID)
		return nil
	}
	if err != nil {
		return transient("failed to look up user for job %s: %w", ev.JobID, err)
	}

	// SQL に連結するカラム名は、ここで選ぶ固定値だけを使う
	jobColumn, urlColumn, objectPrefix := "background_sfsp_job_id", "background_image_url", "backgrounds"
	if isIcon {
		jobColumn, urlColumn, objectPrefix = "icon_sfsp_job_id", "icon_url", "icons"
	}

	// 2. clean 以外の結果なら、画像は反映せずジョブ ID だけクリアする（同じジョブのままの場合のみ）
	if ev.FinalStatus != "clean" {
		if _, err := db.Exec(ctx, "UPDATE users SET "+jobColumn+" = NULL WHERE id = $1 AND "+jobColumn+" = $2", userID, ev.JobID); err != nil {
			return transient("failed to clear scan job for user %s: %w", userID, err)
		}
		log.Printf("Profile image for user %s was not clean (%s)", userID, ev.FinalStatus)
		return nil
	}

	// 3. clean バケット上のオブジェクトキーの候補を並べる（取得できるまで順に試す）
	fileIDStr := ev.FileID.String()
	candidateKeys := []string{
		fileIDStr,
		fmt.Sprintf("%s/%s", fileIDStr, ev.Filename),
	}
	if ev.SHA256 != "" {
		candidateKeys = append(candidateKeys, fmt.Sprintf("%s/%s", ev.SHA256, ev.Filename))
	}
	if ev.Filename != "" {
		candidateKeys = append(candidateKeys, ev.Filename)
	}

	// 4. 一時ファイルにダウンロードする
	tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("profile-%s-%s", userID, fileIDStr))
	defer os.Remove(tmpPath)

	var downloadErr error
	downloadSuccess := false
	for _, objectKey := range candidateKeys {
		downloadErr = sfspCleanMinioClient.FGetObject(ctx, sfspCleanBucketName, objectKey, tmpPath, minio.GetObjectOptions{})
		if downloadErr == nil {
			downloadSuccess = true
			break
		}
	}
	if !downloadSuccess {
		return transient("failed to download clean file for user %s: %w", userID, downloadErr)
	}

	// 5. サイズと画像形式を再検証する（アップロード時の判定とは別に、実際に保存する中身で確認する）
	f, err := os.Open(tmpPath)
	if err != nil {
		return transient("failed to open downloaded file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return transient("failed to stat file: %w", err)
	}
	if stat.Size() > maxImageUploadBytes {
		return fmt.Errorf("profile image for user %s is too large (%d bytes)", userID, stat.Size())
	}

	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	contentType := http.DetectContentType(buffer[:n])
	ext, ok := imageExtensions[contentType]
	if !ok {
		return fmt.Errorf("rejected profile image for user %s with non-image content type %q", userID, contentType)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return transient("failed to reset file: %w", err)
	}

	// 6. 公開バケットへ保存する。キーは "<icons|backgrounds>/<userID>-<ランダムUUID><拡張子>" で、毎回別のキーになる
	objectName := fmt.Sprintf("%s/%s-%s%s", objectPrefix, userID, uuid.New().String(), ext)
	profileURL := fmt.Sprintf("/%s/%s", bucketName, objectName)

	if _, err := minioClient.PutObject(ctx, bucketName, objectName, f, stat.Size(), minio.PutObjectOptions{
		ContentType: contentType,
	}); err != nil {
		return transient("failed to upload profile image to MinIO: %w", err)
	}

	// 7. ジョブ ID が一致する場合だけ URL を差し替え、古い URL を受け取る
	// 副問い合わせで行を FOR UPDATE ロックし、更新前の URL を RETURNING で返す
	// （UPDATE ... RETURNING だけでは更新後の値しか得られないため）
	var oldURL string
	err = db.QueryRow(ctx, `
		UPDATE users u SET `+urlColumn+` = $1, `+jobColumn+` = NULL, updated_at = NOW()
		FROM (SELECT id, COALESCE(`+urlColumn+`, '') AS old_url FROM users WHERE id = $2 FOR UPDATE) o
		WHERE u.id = o.id AND u.`+jobColumn+` = $3
		RETURNING o.old_url`, profileURL, userID, ev.JobID).Scan(&oldURL)
	if err != nil {
		// 反映できなかった画像は残さない
		_ = minioClient.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("Job %s for user %s was superseded or the user was deleted; discarding image", ev.JobID, userID)
			return nil
		}
		return transient("failed to update user profile URL for %s: %w", userID, err)
	}

	// 8. 差し替え前の画像を削除する。失敗しても新しい画像は反映済みなのでログのみ
	if key, ok := objectKeyFromURL(oldURL); ok && key != objectName {
		if err := minioClient.RemoveObject(ctx, bucketName, key, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("Failed to remove old profile image %s: %v", key, err)
		}
	}
	return nil
}

// parseUserIDFromClaims はクレームから string 型 (UUID) の User ID を安全に抽出する。
// "sub"、"userID"、"user_id"、"id" の順に探し、最初に見つかった UUID として正しい値を返す。
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

// authMiddleware は認証用ミドルウェア (UUID対応版)。署名を検証した後、ログアウト済み (blocklist:<jti>) のトークンを拒否する。
// 成功時は userID（と、あれば username）を gin のコンテキストに設定する。
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

		// 署名アルゴリズムを HS256 に固定し、alg を差し替える攻撃（none や RS/HS の取り違え）を防ぐ
		tokenString := strings.TrimSpace(parts[1])
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

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

		// ブロックリストの確認（Redis エラー時は拒否する: fail closed）
		jti, _ := claims["jti"].(string)
		if jti == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		blocked, err := queue.RedisClient.Exists(c.Request.Context(), "blocklist:"+jti).Result()
		if err != nil {
			log.Printf("Redis error checking blocklist: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
			return
		}
		if blocked > 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
			return
		}

		userID, ok := parseUserIDFromClaims(claims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or missing UUID in token"})
			return
		}

		c.Set("userID", userID)
		if username, ok := claims["username"].(string); ok {
			c.Set("username", username)
		}
		c.Next()
	}
}
