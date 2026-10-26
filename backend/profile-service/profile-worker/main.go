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

var (
	db                   *pgxpool.Pool
	minioClient          *minio.Client
	jwtSecret            []byte
	bucketName           string
	sfspApiUrl           string
	sfspApiKey           string
	internalApiKey       string
	maxImageUploadBytes  int64 = 10 << 20
	sfspCleanMinioClient *minio.Client
	sfspCleanBucketName  = "clean-files"
	sfspHTTPClient       = &http.Client{Timeout: 10 * time.Minute}
)

// multipartOverheadBytes は multipart の境界やヘッダー分として、リクエスト全体の上限に上乗せする量。
const multipartOverheadBytes = 1 << 20

// errFileTooLarge はファイルパートが MAX_IMAGE_UPLOAD_BYTES を超えたことを示す。
var errFileTooLarge = errors.New("file exceeds the upload size limit")

type SfspUploadResponse struct {
	FileID   string `json:"file_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
}

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

	if v := os.Getenv("MAX_IMAGE_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			log.Fatalf("Invalid MAX_IMAGE_UPLOAD_BYTES: %q", v)
		}
		maxImageUploadBytes = n
	}

	sfspCleanMinioEndpoint := os.Getenv("SFSP_CLEAN_MINIO_ENDPOINT")
	sfspCleanMinioAccessKey := os.Getenv("SFSP_CLEAN_MINIO_ACCESS_KEY_ID")
	sfspCleanMinioSecretKey := os.Getenv("SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY")
	sfspCleanMinioUseSSL := os.Getenv("SFSP_CLEAN_MINIO_USE_SSL") == "true"

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

	sfspCleanMinioClient, err = minio.New(sfspCleanMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspCleanMinioAccessKey, sfspCleanMinioSecretKey, ""),
		Secure: sfspCleanMinioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to SFSP MinIO: %v", err)
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

	go profileWorker(ctx)

	// ルーティング設定
	r := gin.Default()

	// 内部 API（Nginx からは到達させない。X-Internal-Key 必須）
	internal := r.Group("/internal", internalAuthMiddleware())
	{
		internal.POST("/create", createInternalProfileHandler)
		internal.DELETE("/users/:userId", deleteInternalUserHandler)
	}

	api := r.Group("/api/profile")
	{
		api.GET("/me", authMiddleware(), getMyProfileHandler)
		api.GET("/status", authMiddleware(), getStatusHandler)
		api.GET("/:userId", getProfileHandler)
		api.PUT("", authMiddleware(), updateProfileHandler)
		api.PUT("/icon", authMiddleware(), updateIconHandler)
		api.PUT("/background", authMiddleware(), updateBackgroundHandler)

		// 旧パス（移行期間のみ）。/internal/create と同じく X-Internal-Key が必須
		api.POST("/internal/create", internalAuthMiddleware(), createInternalProfileHandler)
	}

	log.Println("Profile service starting on port 8084")
	if err := r.Run(":8084"); err != nil {
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

	// 削除対象のオブジェクト: DB に記録された URL と、icons/<id>- / backgrounds/<id>- で始まるもの
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

	if _, err := db.Exec(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
		log.Printf("Failed to delete profile row %s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile"})
		return
	}

	log.Printf("Deleted profile %s (%d objects)", uid, len(objects))
	c.Status(http.StatusNoContent)
}

// objectKeyFromURL は "/user-profiles/<key>" 形式の URL からオブジェクトキーを取り出す。
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
	handleProfileImageUpload(c, "icon", "icon_sfsp_job_id", "Icon")
}

func updateBackgroundHandler(c *gin.Context) {
	handleProfileImageUpload(c, "background", "background_sfsp_job_id", "Background")
}

// handleProfileImageUpload は multipart の fieldName のファイルを画像形式・サイズを確認しながら
// sfsp-api にストリーミングで転送し、jobColumn にジョブ ID を保存する。
// jobColumn は呼び出し元の固定値のみ（ユーザー入力を SQL に入れない）。
func handleProfileImageUpload(c *gin.Context, fieldName, jobColumn, label string) {
	userID := c.GetString("userID")

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

	// 先頭 512 バイトで画像形式を判定する（スキャン後にワーカーでも再確認する）
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
type limitedReader struct {
	r         io.Reader
	remaining int64
}

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

func isTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	return errors.Is(err, errFileTooLarge) || errors.As(err, &mbe)
}

// uploadToSFSP は src を multipart で sfsp-api にストリーミング送信する（全体をメモリに載せない）。
func uploadToSFSP(ctx context.Context, src io.Reader, filename, targetService string, resp *SfspUploadResponse) error {
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

	respBytes, err := io.ReadAll(io.LimitReader(sfspResponse.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read SFSP response: %w", err)
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		return fmt.Errorf("SFSP returned non-2xx status %d: %s", sfspResponse.StatusCode, string(respBytes))
	}
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

		if err := queue.Ack(ctx, profileCompletionQueue, payload); err != nil {
			log.Printf("Failed to ack profile event: %v", err)
		}
	}
}

// errTransient は再試行で回復しうる失敗（DB・MinIO の一時的なエラー）を示す。
type errTransient struct{ err error }

func (e errTransient) Error() string { return e.err.Error() }
func (e errTransient) Unwrap() error { return e.err }

func transient(format string, args ...any) error {
	return errTransient{fmt.Errorf(format, args...)}
}

// processProfileEventWithRetry は一時的な失敗なら数回再試行し、それでも失敗したら
// ジョブ ID をクリアして終端状態にする（フロントエンドのポーリングを終わらせるため）。
func processProfileEventWithRetry(ctx context.Context, payload string) {
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		log.Printf("ERROR: Unmarshalling event payload: %v", err)
		return
	}

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
func processProfileEvent(ctx context.Context, ev event.ScanCompletionEvent) error {
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

	jobColumn, urlColumn, objectPrefix := "background_sfsp_job_id", "background_image_url", "backgrounds"
	if isIcon {
		jobColumn, urlColumn, objectPrefix = "icon_sfsp_job_id", "icon_url", "icons"
	}

	if ev.FinalStatus != "clean" {
		if _, err := db.Exec(ctx, "UPDATE users SET "+jobColumn+" = NULL WHERE id = $1 AND "+jobColumn+" = $2", userID, ev.JobID); err != nil {
			return transient("failed to clear scan job for user %s: %w", userID, err)
		}
		log.Printf("Profile image for user %s was not clean (%s)", userID, ev.FinalStatus)
		return nil
	}

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

	objectName := fmt.Sprintf("%s/%s-%s%s", objectPrefix, userID, uuid.New().String(), ext)
	profileURL := fmt.Sprintf("/%s/%s", bucketName, objectName)

	if _, err := minioClient.PutObject(ctx, bucketName, objectName, f, stat.Size(), minio.PutObjectOptions{
		ContentType: contentType,
	}); err != nil {
		return transient("failed to upload profile image to MinIO: %w", err)
	}

	// ジョブ ID が一致する場合だけ URL を差し替え、古い URL を受け取る
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

	if key, ok := objectKeyFromURL(oldURL); ok && key != objectName {
		if err := minioClient.RemoveObject(ctx, bucketName, key, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("Failed to remove old profile image %s: %v", key, err)
		}
	}
	return nil
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

// 認証用ミドルウェア (UUID対応版)。署名を検証した後、ログアウト済み (blocklist:<jti>) のトークンを拒否する
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
