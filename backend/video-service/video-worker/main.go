package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
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
	sfspApiUrl           string
	sfspApiKey           string
	sfspCleanMinioClient *minio.Client
	sfspCleanBucketName  = "clean-files"
	videoMinioClient     *minio.Client
	videoBucketName      = "videos"
	logger               *zap.SugaredLogger
	jwtSecret            []byte
	teamServiceURL       string
	// maxImageUploadBytes はサムネイル画像の上限（MAX_IMAGE_UPLOAD_BYTES、既定 10 MiB）。
	maxImageUploadBytes = int64(10 << 20)
)

const (
	StatusPublic = "public"
	// multipartOverhead はサムネイル以外のフォーム（title / description と境界）に許す余裕。
	multipartOverhead = 1 << 20
)

type Video struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Filename           string     `json:"filename"`
	ThumbnailPath      string     `json:"thumbnail_path"`
	UploaderID         string     `json:"uploader_id"`
	UploaderName       string     `json:"uploader_name"`
	TeamID             string     `json:"team_id"`
	Status             string     `json:"status"`
	ProcessingDetails  string     `json:"processing_details"`
	ThumbnailSfspJobID *uuid.UUID `json:"thumbnail_sfsp_job_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

// 💡 動画更新時のリクエストボディ構造体
type UpdateVideoInput struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
}

type SfspUploadResponse struct {
	FileID   string `json:"file_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
}

// imageExtensions は保存を許可する画像の Content-Type と拡張子の対応。
// HTML/SVG 等を「サムネイル」として保存させないため、これ以外は拒否する。
var imageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func envInt64(name string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		log.Printf("WARN: invalid %s=%q, using default %d", name, v, def)
		return def
	}
	return n
}

func main() {
	var err error
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	ctx := context.Background()

	databaseUrl := os.Getenv("DATABASE_URL")
	if databaseUrl == "" {
		logger.Fatal("DATABASE_URL environment variable is not set")
	}

	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		logger.Fatal("JWT_SECRET environment variable is not set")
	}

	sfspApiUrl = os.Getenv("SFSP_API_URL")
	if sfspApiUrl == "" {
		logger.Fatal("SFSP_API_URL environment variable is not set")
	}
	sfspApiKey = os.Getenv("SFSP_API_KEY")
	if sfspApiKey == "" {
		logger.Warn("SFSP_API_KEY is not set; requests to sfsp-api will be rejected if it requires an API key")
	}
	teamServiceURL = os.Getenv("TEAM_SERVICE_URL")
	if teamServiceURL == "" {
		teamServiceURL = "http://team-service:8080"
	}
	teamServiceURL = strings.TrimRight(teamServiceURL, "/")
	maxImageUploadBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", 10<<20)

	sfspCleanMinioEndpoint := os.Getenv("SFSP_CLEAN_MINIO_ENDPOINT")
	sfspCleanMinioAccessKey := os.Getenv("SFSP_CLEAN_MINIO_ACCESS_KEY_ID")
	sfspCleanMinioSecretKey := os.Getenv("SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY")
	sfspCleanMinioUseSSL := os.Getenv("SFSP_CLEAN_MINIO_USE_SSL") == "true"

	if sfspCleanMinioEndpoint == "" || sfspCleanMinioAccessKey == "" || sfspCleanMinioSecretKey == "" {
		logger.Fatal("FATAL: Configuration for Clean MinIO is incomplete. SFSP_CLEAN_MINIO_ENDPOINT, SFSP_CLEAN_MINIO_ACCESS_KEY_ID, and SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY must all be set.")
	}

	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		logger.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	sfspCleanMinioClient, err = minio.New(sfspCleanMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspCleanMinioAccessKey, sfspCleanMinioSecretKey, ""),
		Secure: sfspCleanMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to SFSP Clean MinIO: %v", err)
	}
	logger.Info("Successfully connected to SFSP Clean MinIO!")

	videoMinioEndpoint := os.Getenv("VIDEO_MINIO_ENDPOINT")
	videoMinioAccessKey := os.Getenv("VIDEO_MINIO_ACCESS_KEY_ID")
	videoMinioSecretKey := os.Getenv("VIDEO_MINIO_SECRET_ACCESS_KEY")
	videoMinioUseSSL := os.Getenv("VIDEO_MINIO_USE_SSL") == "true"

	if videoMinioEndpoint == "" || videoMinioAccessKey == "" || videoMinioSecretKey == "" {
		logger.Fatal("FATAL: Configuration for Video MinIO is incomplete. VIDEO_MINIO_ENDPOINT, VIDEO_MINIO_ACCESS_KEY_ID, and VIDEO_MINIO_SECRET_ACCESS_KEY must all be set.")
	}

	videoMinioClient, err = minio.New(videoMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(videoMinioAccessKey, videoMinioSecretKey, ""),
		Secure: videoMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to Video MinIO: %v", err)
	}
	logger.Info("Successfully connected to Video MinIO!")

	// 前回の異常終了で Ack されずに残ったイベントを本来のキューへ戻してから消費を始める。
	if n, err := queue.RecoverProcessing(ctx, queue.VideoThumbnailCompletionQueue); err != nil {
		logger.Errorf("Failed to recover unacknowledged events from %s: %v", queue.ProcessingQueueName(queue.VideoThumbnailCompletionQueue), err)
	} else if n > 0 {
		logger.Warnf("Recovered %d unacknowledged event(s) back to %s", n, queue.VideoThumbnailCompletionQueue)
	}

	go videoThumbnailWorker(ctx)

	r := gin.Default()
	r.MaxMultipartMemory = 8 << 20
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"http://localhost:3001"} // フロントエンドのURL
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	config.AllowCredentials = true
	r.Use(cors.New(config))

	api := r.Group("/api/videos")
	{
		api.GET("", listVideosHandler)
		api.GET("/:id", videoDetailsHandler)
		api.PUT("/:id", authMiddleware(), updateVideoHandler)
	}

	logger.Info("Stream service (metadata) starting on port 8081")
	if err := r.Run(":8081"); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

// ------------------------------------------------------------------
// 認証
// ------------------------------------------------------------------

var (
	errNoToken      = errors.New("no token")
	errInvalidToken = errors.New("invalid token")
	errRevoked      = errors.New("token has been invalidated")
)

// parseToken は Authorization ヘッダーの JWT（HS256 固定）を検証し、ブロックリスト（blocklist:<jti>）を確認する。
// Redis エラーは error としてそのまま返す（呼び出し側で fail closed にする）。
func parseToken(c *gin.Context) (userID string, isAdmin bool, err error) {
	authHeader := c.GetHeader("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false, errNoToken
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return "", false, errInvalidToken
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", false, errInvalidToken
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return "", false, errInvalidToken
	}
	exists, rerr := queue.RedisClient.Exists(c.Request.Context(), "blocklist:"+jti).Result()
	if rerr != nil {
		return "", false, fmt.Errorf("redis blocklist check failed: %w", rerr)
	}
	if exists > 0 {
		return "", false, errRevoked
	}
	for _, key := range []string{"sub", "userID", "user_id"} {
		if val, ok := claims[key].(string); ok && val != "" {
			userID = val
			break
		}
	}
	if userID == "" {
		return "", false, errInvalidToken
	}
	isAdmin, _ = claims["isAdmin"].(bool)
	return userID, isAdmin, nil
}

// authMiddleware は JWT を必須とし、userID / isAdmin をコンテキストに設定する。Redis エラー時は拒否する（fail closed）。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, isAdmin, err := parseToken(c)
		switch {
		case err == nil:
		case errors.Is(err, errNoToken):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		case errors.Is(err, errRevoked):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
			return
		case errors.Is(err, errInvalidToken):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		default:
			logger.Errorf("authMiddleware: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
			return
		}
		c.Set("userID", userID)
		c.Set("isAdmin", isAdmin)
		c.Next()
	}
}

// optionalUser は公開 API 向け。トークンが無い・無効・失効済み・検証不能（Redis エラー）なら匿名として扱う。
func optionalUser(c *gin.Context) (userID string, isAdmin bool) {
	userID, isAdmin, err := parseToken(c)
	if err != nil {
		if !errors.Is(err, errNoToken) && !errors.Is(err, errInvalidToken) && !errors.Is(err, errRevoked) {
			logger.Warnf("optionalUser: treating caller as anonymous: %v", err)
		}
		return "", false
	}
	return userID, isAdmin
}

// teamCanView は team-service に呼び出し元の Authorization ヘッダーを転送し、チームの閲覧権限を確認する。
// 失敗時は閲覧不可として扱う。
func teamCanView(c *gin.Context, teamToken string) bool {
	if teamToken == "" {
		return true
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", teamServiceURL+"/api/teams/"+url.PathEscape(teamToken)+"/permission", nil)
	if err != nil {
		logger.Errorf("teamCanView: failed to create request: %v", err)
		return false
	}
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Errorf("teamCanView: request failed for team %s: %v", teamToken, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warnf("teamCanView: team service returned %d for team %s", resp.StatusCode, teamToken)
		return false
	}
	var body struct {
		CanView bool `json:"can_view"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		logger.Errorf("teamCanView: failed to decode response: %v", err)
		return false
	}
	return body.CanView
}

// ------------------------------------------------------------------
// 参照 API
// ------------------------------------------------------------------

// 動画一覧・検索
// 公開済み（status = 'public'）だけを返す。?team= が無ければ全体公開（team_id IS NULL）のみ、
// ?team= があれば team-service で can_view を確認したうえでそのチーム限定の動画を返す。
func listVideosHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	teamToken := c.Query("team")

	whereConds := []string{"status = $1"}
	args := []interface{}{StatusPublic}
	argIdx := 2
	if teamToken != "" {
		if !teamCanView(c, teamToken) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to view this team's videos"})
			return
		}
		whereConds = append(whereConds, "team_id = $"+strconv.Itoa(argIdx))
		args = append(args, teamToken)
		argIdx++
	} else {
		whereConds = append(whereConds, "team_id IS NULL")
	}
	if searchTerm != "" {
		whereConds = append(whereConds, "title ILIKE $"+strconv.Itoa(argIdx))
		args = append(args, "%"+searchTerm+"%")
		argIdx++
	}
	whereClause := "WHERE " + strings.Join(whereConds, " AND ")

	// 💡 COALESCE(description, '') で NULL 対策を追加
	rows, err := db.Query(c.Request.Context(),
		`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, COALESCE(team_id, ''), status, COALESCE(processing_details, '') as processing_details, created_at
         FROM videos
         `+whereClause+`
         ORDER BY created_at DESC LIMIT 50`, args...)
	if err != nil {
		logger.Errorf("listVideosHandler: query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database query failed"})
		return
	}
	defer rows.Close()
	videos := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.TeamID, &v.Status, &v.ProcessingDetails, &v.CreatedAt); err != nil {
			log.Printf("Error scanning video row: %v", err)
			continue
		}
		videos = append(videos, v)
	}
	c.JSON(http.StatusOK, videos)
}

// 動画詳細取得
// 投稿者本人と管理者は状態にかかわらず閲覧できる。それ以外は status = 'public' のものだけで、
// team_id があればチームの can_view も必要。見せられない場合は存在を明かさないよう 404 を返す。
func videoDetailsHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	var v Video
	// 💡 COALESCE(description, '') で NULL 対策を追加
	err := db.QueryRow(c.Request.Context(),
		`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, COALESCE(team_id, ''), status, COALESCE(processing_details, '') as processing_details, thumbnail_sfsp_job_id, created_at
        FROM videos
        WHERE id = $1`, videoID).Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.TeamID, &v.Status, &v.ProcessingDetails, &v.ThumbnailSfspJobID, &v.CreatedAt)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Errorf("videoDetailsHandler: query failed for ID %s: %v", videoID, err)
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	userID, isAdmin := optionalUser(c)
	isOwner := userID != "" && strings.EqualFold(userID, v.UploaderID)
	if !isOwner && !isAdmin {
		if v.Status != StatusPublic || !teamCanView(c, v.TeamID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
			return
		}
		// 失敗理由などの内部情報は投稿者以外に見せない。
		v.ProcessingDetails = ""
		v.ThumbnailSfspJobID = nil
	}
	c.JSON(http.StatusOK, v)
}

// ------------------------------------------------------------------
// 更新 API
// ------------------------------------------------------------------

// 💡 動画更新（PUT /api/videos/:id）
// title/description を multipart から受取り、thumbnail が存在すれば SFSP スキャンへ投入する。
func updateVideoHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	// 投稿者本人（または管理者）のみ更新可能
	var uploaderID string
	err := db.QueryRow(c.Request.Context(), "SELECT uploader_id FROM videos WHERE id = $1", videoID).Scan(&uploaderID)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
			return
		}
		logger.Errorf("ERROR: Failed to look up uploader for video ID %s: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video"})
		return
	}
	if !strings.EqualFold(uploaderID, c.GetString("userID")) && !c.GetBool("isAdmin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to edit this video"})
		return
	}

	tooLarge := gin.H{"error": fmt.Sprintf("Thumbnail is too large. The maximum size is %d bytes.", maxImageUploadBytes)}
	if c.Request.ContentLength > maxImageUploadBytes+multipartOverhead {
		c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageUploadBytes+multipartOverhead)
	if strings.HasPrefix(c.ContentType(), "multipart/") {
		if err := c.Request.ParseMultipartForm(8 << 20); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart form"})
			return
		}
		defer func() {
			if c.Request.MultipartForm != nil {
				_ = c.Request.MultipartForm.RemoveAll()
			}
		}()
	} else if err := c.Request.ParseForm(); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid form"})
		return
	}

	// サムネイルがあれば、DB を更新する前にサイズと形式を確認する。
	var thumbFile multipart.File
	var thumbName string
	if f, header, ferr := c.Request.FormFile("thumbnail"); ferr == nil {
		defer f.Close()
		if header.Size > maxImageUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		head := make([]byte, 512)
		n, rerr := io.ReadFull(f, head)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read thumbnail"})
			return
		}
		ext, ok := imageExtensions[http.DetectContentType(head[:n])]
		if !ok {
			c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Thumbnail must be a JPEG, PNG, GIF or WebP image"})
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process upload file"})
			return
		}
		thumbFile = f
		thumbName = "thumbnail" + ext
	}

	title := c.Request.FormValue("title")
	description := c.Request.FormValue("description")

	_, err = db.Exec(c.Request.Context(),
		"UPDATE videos SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
		title, description, videoID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update video ID %s: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video"})
		return
	}

	if thumbFile == nil {
		// サムネイルなし（タイトル/説明のみ更新）
		c.JSON(http.StatusOK, gin.H{"message": "Video updated successfully"})
		return
	}

	var sfspResp SfspUploadResponse
	if err := uploadToSFSP(c.Request.Context(), thumbFile, thumbName, "video-thumbnail", &sfspResp); err != nil {
		logger.Errorf("Error forwarding thumbnail to SFSP: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File scanning service is unavailable or request timed out"})
		return
	}

	sfspJobID, err := uuid.Parse(sfspResp.JobID)
	if err != nil {
		logger.Errorf("Invalid Job ID from SFSP: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid Job ID from SFSP"})
		return
	}

	_, err = db.Exec(c.Request.Context(),
		"UPDATE videos SET thumbnail_sfsp_job_id = $1, updated_at = NOW() WHERE id = $2",
		sfspJobID, videoID)
	if err != nil {
		logger.Errorf("Error updating video thumbnail SFSP job ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Thumbnail upload accepted for scanning",
		"job_id":  sfspJobID.String(),
		"status":  "scanning",
	})
}

// uploadToSFSP はファイルをメモリに溜めず io.Pipe で multipart として SFSP へ流し込み、応答を resp に格納する。
// sfsp-api の /api/v1/* に必要な X-SFSP-API-Key を付ける。
func uploadToSFSP(ctx context.Context, src io.Reader, filename, targetService string, resp *SfspUploadResponse) error {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

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
		pw.CloseWithError(err)
	}()

	sfspRequest, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", pr)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("failed to create SFSP request: %w", err)
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if sfspApiKey != "" {
		sfspRequest.Header.Set("X-SFSP-API-Key", sfspApiKey)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	sfspResponse, err := client.Do(sfspRequest)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("SFSP request failed: %w", err)
	}
	defer sfspResponse.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(sfspResponse.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read SFSP response: %w", err)
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		return fmt.Errorf("SFSP returned non-2xx status %d: %s", sfspResponse.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return fmt.Errorf("failed to decode SFSP response: %w", err)
	}

	return nil
}

// ------------------------------------------------------------------
// サムネイル差し替えのワーカー
// ------------------------------------------------------------------

func ackThumbnail(payload string) {
	if err := queue.Ack(context.Background(), queue.VideoThumbnailCompletionQueue, payload); err != nil {
		logger.Errorf("Failed to ack event on %s: %v", queue.VideoThumbnailCompletionQueue, err)
	}
}

// videoThumbnailWorker は SFSP からのサムネイル完了イベントを BRPOPLPUSH で取り出し、クリーンなサムネイルを
// video-storage へ展開した後、videos.thumbnail_path を更新し thumbnail_sfsp_job_id を消去する。
// thumbnail_sfsp_job_id が消えた（＝終端状態になった）時点で Ack する。失敗しても再試行はしない。
func videoThumbnailWorker(ctx context.Context) {
	logger.Info("Video Thumbnail Worker started. Waiting for thumbnail completion events...")
	q := queue.VideoThumbnailCompletionQueue

	for {
		payload, err := queue.DequeueReliable(ctx, q, 0)
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			logger.Errorf("Error fetching event from Redis: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if handleThumbnailEvent(ctx, payload) {
			ackThumbnail(payload)
		}
	}
}

// clearThumbnailJob は thumbnail_sfsp_job_id を消して終端状態にする。成功したら true。
func clearThumbnailJob(ctx context.Context, videoID string, jobID uuid.UUID) bool {
	_, err := db.Exec(ctx, "UPDATE videos SET thumbnail_sfsp_job_id = NULL, updated_at = NOW() WHERE id = $1 AND thumbnail_sfsp_job_id = $2", videoID, jobID)
	if err != nil {
		logger.Errorf("[VideoID: %s] Failed to clear thumbnail scan job: %v", videoID, err)
		return false
	}
	return true
}

// handleThumbnailEvent は 1 件のイベントを処理し、Ack してよいか（DB が終端状態になったか）を返す。
func handleThumbnailEvent(ctx context.Context, payload string) bool {
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		logger.Errorf("ERROR: Unmarshalling event payload (dropping): %v", err)
		return true
	}

	if ev.TargetService != "video-thumbnail" {
		logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a video thumbnail event.", ev.TargetService, ev.JobID)
		return true
	}

	// PUT ハンドラーが thumbnail_sfsp_job_id を保存する前にスキャンが終わることがあるため、少し待って再試行する。
	var videoID, currentThumbnailPath string
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		err = db.QueryRow(ctx, "SELECT id, COALESCE(thumbnail_path, '') FROM videos WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", ev.JobID).Scan(&videoID, &currentThumbnailPath)
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if attempt < 5 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Errorf("[Thumbnail JobID: %s] Could not find a matching video record (deleted or superseded). Dropping event.", ev.JobID)
			return true
		}
		// DB 障害。Ack せず処理中リストに残し、次回起動時に回収させる。
		logger.Errorf("[Thumbnail JobID: %s] ERROR: Failed to look up video record: %v", ev.JobID, err)
		return false
	}

	if ev.FinalStatus != "clean" {
		logger.Infof("[VideoID: %s] Thumbnail scan result is '%s'. Aborting thumbnail processing.", videoID, ev.FinalStatus)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}

	logger.Infof("[VideoID: %s] Thumbnail is clean. Processing thumbnail...", videoID)

	fileIDStr := ev.FileID.String()
	candidateKeys := []string{
		fileIDStr,
		fmt.Sprintf("%s/%s", fileIDStr, ev.Filename),
	}

	// 一時ファイル名にユーザーのファイル名は使わない。
	tmp, err := os.CreateTemp("", "video-thumb-*")
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to create temp file: %v", videoID, err)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}
	tempThumbPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tempThumbPath)

	var downloadErr error
	downloadSuccess := false

	// The SFSP scan completion event can arrive before the clean file is
	// fully written to the MinIO bucket (a false-start / flying event).
	// Retry the download a few times before giving up so a premature event
	// does not permanently skip the thumbnail.
	const maxRetries = 10
	const retryDelay = 3 * time.Second
	for attempt := 0; attempt <= maxRetries && !downloadSuccess; attempt++ {
		if attempt > 0 {
			logger.Infof("[VideoID: %s] Retry %d/%d downloading thumbnail from SFSP...", videoID, attempt, maxRetries)
			time.Sleep(retryDelay)
		}
		for _, objectKey := range candidateKeys {
			downloadErr = sfspCleanMinioClient.FGetObject(ctx, sfspCleanBucketName, objectKey, tempThumbPath, minio.GetObjectOptions{})
			if downloadErr == nil {
				downloadSuccess = true
				logger.Infof("[VideoID: %s] Successfully downloaded thumbnail with key '%s'", videoID, objectKey)
				break
			}
			logger.Warnf("[VideoID: %s] Failed to download thumbnail key '%s': %v", videoID, objectKey, downloadErr)
		}
	}

	if !downloadSuccess {
		logger.Errorf("[VideoID: %s] ERROR: Download thumbnail from SFSP failed after %d retries: %v", videoID, maxRetries, downloadErr)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}

	if st, err := os.Stat(tempThumbPath); err != nil || st.Size() > maxImageUploadBytes {
		logger.Errorf("[VideoID: %s] ERROR: Downloaded thumbnail is unreadable or exceeds the size limit", videoID)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}
	data, err := os.ReadFile(tempThumbPath)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to read downloaded thumbnail: %v", videoID, err)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}

	// 画像以外は保存しない（既存サムネイルを消す前に判定する）
	contentType := http.DetectContentType(data)
	if _, ok := imageExtensions[contentType]; !ok {
		logger.Errorf("[VideoID: %s] ERROR: Rejected thumbnail with non-image content type %q", videoID, contentType)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}

	// Name the thumbnail object after the video ID so it is directly identifiable
	// with its video (mirrors videos/<videoID>/). Deterministic naming lets
	// us reclaim storage by deleting the previous thumbnail first.
	thumbnailObjectName := fmt.Sprintf("thumbnails/%s", videoID)

	// Reclaim storage: delete the previous thumbnail if it used a different object name
	// (the new one overwrites thumbnails/<videoID> in place).
	if currentThumbnailPath != "" {
		oldObjectName := strings.TrimPrefix(currentThumbnailPath, "/videos/")
		if oldObjectName != thumbnailObjectName {
			if err := videoMinioClient.RemoveObject(ctx, videoBucketName, oldObjectName, minio.RemoveObjectOptions{}); err != nil {
				logger.Warnf("[VideoID: %s] WARNING: Failed to delete previous thumbnail %s: %v", videoID, oldObjectName, err)
			} else {
				logger.Infof("[VideoID: %s] Deleted previous thumbnail %s", videoID, oldObjectName)
			}
		}
	}

	_, err = videoMinioClient.PutObject(ctx, videoBucketName, thumbnailObjectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to upload thumbnail to MinIO: %v", videoID, err)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}

	thumbnailURL := "/videos/" + thumbnailObjectName
	tag, err := db.Exec(ctx, "UPDATE videos SET thumbnail_path = $1, thumbnail_sfsp_job_id = NULL, updated_at = NOW() WHERE id = $2", thumbnailURL, videoID)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to update thumbnail URL in DB: %v", videoID, err)
		return clearThumbnailJob(ctx, videoID, ev.JobID)
	}
	if tag.RowsAffected() == 0 {
		// 処理中に動画が削除された。アップロードしたサムネイルを片付ける。
		if err := videoMinioClient.RemoveObject(ctx, videoBucketName, thumbnailObjectName, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("[VideoID: %s] Failed to remove thumbnail of deleted video: %v", videoID, err)
		}
		return true
	}

	logger.Infof("[VideoID: %s] Thumbnail processing complete. Stored at %s", videoID, thumbnailURL)
	return true
}
