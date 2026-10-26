package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db                   *pgxpool.Pool
	jwtSecret            []byte
	uploadDir            string
	thumbnailDir         string
	sfspApiUrl           string
	sfspApiKey           string
	sfspCleanMinioClient *minio.Client
	sfspCleanBucketName  string
	videoMinioClient     *minio.Client
	videoBucketName      = "videos"
	// maxVideoUploadBytes は動画ファイル本体の上限（MAX_VIDEO_UPLOAD_BYTES、既定 1 GiB）。
	maxVideoUploadBytes = int64(1 << 30)
	// ffmpegSem は同時に実行する変換ジョブ（= ffmpeg）の数を FFMPEG_MAX_CONCURRENCY に制限する。
	ffmpegSem chan struct{}
	// ffmpegTimeout は ffmpeg 1 回の実行に許す最大時間（FFMPEG_TIMEOUT、既定 2h）。
	ffmpegTimeout         = 2 * time.Hour
	logger                *zap.SugaredLogger
	allowedVideoMimeTypes = map[string]bool{
		"video/mp4":        true,
		"video/webm":       true,
		"video/quicktime":  true,
		"video/x-msvideo":  true,
		"video/x-matroska": true,
	}
	teamServiceURL string

	// jobs は処理中の変換ジョブのキャンセル関数（動画 ID がキー）。削除ハンドラーが呼ぶ。
	jobsMu sync.Mutex
	jobs   = map[string]context.CancelFunc{}
)

// multipartOverhead は動画ファイル以外のフォーム（title / description / team_id と境界）に許す余裕。
const multipartOverhead = 1 << 20

// teamAccessCheck calls the team-service permission endpoint to verify that the
// caller is allowed to post to the given team. Returns true if posting is allowed.
// A missing/empty team_id is treated as a public post (always allowed).
func teamAccessCheck(c *gin.Context, teamToken string) bool {
	if teamToken == "" {
		return true
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", teamServiceURL+"/api/teams/"+url.PathEscape(teamToken)+"/permission", nil)
	if err != nil {
		logger.Errorf("teamAccessCheck: failed to create request: %v", err)
		return false
	}
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Errorf("teamAccessCheck: request failed for team %s: %v", teamToken, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("teamAccessCheck: team service returned %d for team %s", resp.StatusCode, teamToken)
		return false
	}
	var body struct {
		CanPost bool `json:"can_post"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		logger.Errorf("teamAccessCheck: failed to decode response: %v", err)
		return false
	}
	return body.CanPost
}

const (
	StatusScanning    = "scanning"
	StatusProcessing  = "processing"
	StatusPublic      = "public"
	StatusError       = "error"
	StatusQuarantined = "quarantined"
)

type Video struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Filename          string `json:"filename"`
	ThumbnailPath     string `json:"thumbnail_path"`
	UploaderID        string `json:"uploader_id"`
	TeamID            string `json:"team_id"`
	Status            string `json:"status"`
	ProcessingDetails string `json:"processing_details"`
}

type SfspUploadResponse struct {
	FileID   string `json:"file_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
}

func generateRandomID() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// envInt64 / envInt / envDuration は環境変数を読み、未設定・不正・0 以下なら既定値を返す。
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

func envDuration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("WARN: invalid %s=%q, using default %s", name, v, def)
		return def
	}
	return d
}

// safeExt は拡張子を英数字だけ（最大 10 文字）に制限して返す。条件を満たさなければ空文字。
func safeExt(filename string) string {
	ext := filepath.Ext(filepath.Base(filename))
	if len(ext) < 2 || len(ext) > 11 {
		return ""
	}
	for _, r := range ext[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return strings.ToLower(ext)
}

// displayFilename はユーザーのファイル名からディレクトリ部分と制御文字を除き、VARCHAR(255) に収まるよう切り詰める。
// ローカルのファイル名には使わない（DB の表示用と SFSP へのフォーム用）。
func displayFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "video"
	}
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// setSFSPAuth は sfsp-api の /api/v1/* に必要な API キーを付ける。
func setSFSPAuth(req *http.Request) {
	if sfspApiKey != "" {
		req.Header.Set("X-SFSP-API-Key", sfspApiKey)
	}
}

// postFileToSFSP は r の内容をメモリや一時ファイルに溜めず、io.Pipe で multipart として sfsp-api に流し込む。
// 戻り値は (応答, HTTP ステータス, 応答本文, エラー)。
func postFileToSFSP(ctx context.Context, r io.Reader, filename, targetService string) (*SfspUploadResponse, int, string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		err := func() error {
			if err := mw.WriteField("target_service", targetService); err != nil {
				return err
			}
			part, err := mw.CreateFormFile("file", filename)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, r); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", pr)
	if err != nil {
		pr.CloseWithError(err)
		return nil, 0, "", fmt.Errorf("failed to create SFSP request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setSFSPAuth(req)

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		pr.CloseWithError(err)
		return nil, 0, "", fmt.Errorf("SFSP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("failed to read SFSP response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, string(respBytes), fmt.Errorf("SFSP returned non-2xx status %d", resp.StatusCode)
	}
	var data SfspUploadResponse
	if err := json.Unmarshal(respBytes, &data); err != nil {
		return nil, resp.StatusCode, string(respBytes), fmt.Errorf("failed to decode SFSP response: %w", err)
	}
	return &data, resp.StatusCode, string(respBytes), nil
}

func ensureDatabaseExists(databaseUrl string) error {
	config, err := pgxpool.ParseConfig(databaseUrl)
	if err != nil {
		return fmt.Errorf("failed to parse DATABASE_URL: %w", err)
	}

	targetDB := config.ConnConfig.Database
	if targetDB == "" || targetDB == "postgres" {
		return nil
	}

	adminDSN := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		config.ConnConfig.Host,
		config.ConnConfig.Port,
		config.ConnConfig.User,
		config.ConnConfig.Password,
	)

	adminDB, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("failed to connect to admin postgres db: %w", err)
	}
	defer adminDB.Close()

	var exists bool
	if err := adminDB.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", targetDB).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check database existence: %w", err)
	}

	if !exists {
		logger.Infof("Database '%s' does not exist. Creating...", targetDB)
		_, err = adminDB.Exec(fmt.Sprintf("CREATE DATABASE \"%s\"", strings.ReplaceAll(targetDB, "\"", "\"\"")))
		if err != nil {
			return fmt.Errorf("failed to create database '%s': %w", targetDB, err)
		}
		logger.Infof("Database '%s' created successfully.", targetDB)
	}

	return nil
}

// initTables は init.sql が流れていない環境向けの予備。定義は backend/datebase/app-db/init.sql の videos に合わせる。
func initTables(ctx context.Context, pool *pgxpool.Pool) error {
	createVideosTableSQL := `
    CREATE TABLE IF NOT EXISTS videos (
       id VARCHAR(10) PRIMARY KEY,
       uploader_id UUID NOT NULL,
       title VARCHAR(255) NOT NULL,
       description TEXT,
       filename VARCHAR(255) NOT NULL,
       thumbnail_path VARCHAR(255),
       status VARCHAR(50) NOT NULL DEFAULT 'processing',
       sfsp_job_id UUID,
       thumbnail_sfsp_job_id UUID,
       processing_details TEXT,
       team_id VARCHAR(24),
       created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
       updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
    );`

	if _, err := pool.Exec(ctx, createVideosTableSQL); err != nil {
		return fmt.Errorf("failed to create videos table: %w", err)
	}

	// 既存DBへの後方互換性のため、足りないカラムを追加する。
	// team_idがNULLの動画が全体公開、チームトークンを保持する動画がそのチーム限定公開になる。
	alters := []string{
		`ALTER TABLE videos ADD COLUMN IF NOT EXISTS team_id VARCHAR(24)`,
		`ALTER TABLE videos ADD COLUMN IF NOT EXISTS thumbnail_sfsp_job_id UUID`,
		`ALTER TABLE videos ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_sfsp_job_id ON videos(sfsp_job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_thumbnail_sfsp_job_id ON videos(thumbnail_sfsp_job_id)`,
	}
	for _, stmt := range alters {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to migrate videos table (%s): %w", stmt, err)
		}
	}

	logger.Info("Database tables initialized successfully.")
	return nil
}

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	ctx := context.Background()
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		logger.Fatal("JWT_SECRET environment variable is not set")
	}
	uploadDir = os.Getenv("UPLOAD_DIR")
	thumbnailDir = os.Getenv("THUMBNAIL_DIR")
	databaseUrl := os.Getenv("DATABASE_URL")
	sfspApiUrl = os.Getenv("SFSP_API_URL")
	sfspApiKey = os.Getenv("SFSP_API_KEY")
	if sfspApiKey == "" {
		logger.Warn("SFSP_API_KEY is not set; requests to sfsp-api will be rejected if it requires an API key")
	}
	teamServiceURL = os.Getenv("TEAM_SERVICE_URL")
	if teamServiceURL == "" {
		teamServiceURL = "http://team-service:8080"
	}
	teamServiceURL = strings.TrimRight(teamServiceURL, "/")

	maxVideoUploadBytes = envInt64("MAX_VIDEO_UPLOAD_BYTES", 1<<30)
	ffmpegSem = make(chan struct{}, int(envInt64("FFMPEG_MAX_CONCURRENCY", 2)))
	ffmpegTimeout = envDuration("FFMPEG_TIMEOUT", 2*time.Hour)
	logger.Infof("Limits: MAX_VIDEO_UPLOAD_BYTES=%d FFMPEG_MAX_CONCURRENCY=%d FFMPEG_TIMEOUT=%s", maxVideoUploadBytes, cap(ffmpegSem), ffmpegTimeout)

	sfspCleanBucketName = os.Getenv("SFSP_CLEAN_BUCKET_NAME")
	if sfspCleanBucketName == "" {
		sfspCleanBucketName = "clean-files"
	}

	if sfspApiUrl == "" {
		logger.Fatalf("SFSP_API_URL environment variable is not set")
	}

	sfspCleanMinioEndpoint := os.Getenv("SFSP_CLEAN_MINIO_ENDPOINT")
	sfspCleanMinioAccessKey := os.Getenv("SFSP_CLEAN_MINIO_ACCESS_KEY_ID")
	sfspCleanMinioSecretKey := os.Getenv("SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY")
	sfspCleanMinioUseSSL := os.Getenv("SFSP_CLEAN_MINIO_USE_SSL") == "true"

	if sfspCleanMinioEndpoint == "" || sfspCleanMinioAccessKey == "" || sfspCleanMinioSecretKey == "" {
		logger.Fatalf("FATAL: Configuration for Clean MinIO is incomplete. SFSP_CLEAN_MINIO_ENDPOINT, SFSP_CLEAN_MINIO_ACCESS_KEY_ID, and SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY must all be set.")
	}

	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		logger.Fatalf("Failed to create upload directory: %v", err)
	}
	if err := os.MkdirAll(thumbnailDir, os.ModePerm); err != nil {
		logger.Fatalf("Failed to create thumbnail directory: %v", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	for i := 0; i < 5; i++ {
		err = ensureDatabaseExists(databaseUrl)
		if err == nil {
			break
		}
		logger.Warnf("Failed to ensure database existence, retrying in 5 seconds... (%d/5): %v", i+1, err)
		time.Sleep(5 * time.Second)
	}

	for i := 0; i < 5; i++ {
		db, err = pgxpool.New(ctx, databaseUrl)
		if err == nil {
			err = db.Ping(ctx)
			if err == nil {
				break
			}
		}
		logger.Warnf("Failed to connect to database, retrying in 5 seconds... (%d/5)", i+1)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		logger.Fatalf("Failed to connect to database after multiple retries: %v", err)
	}
	defer db.Close()
	logger.Info("Successfully connected to PostgreSQL!")

	if err := initTables(ctx, db); err != nil {
		logger.Fatalf("Failed to initialize database tables: %v", err)
	}

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	sfspCleanMinioClient, err = minio.New(sfspCleanMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspCleanMinioAccessKey, sfspCleanMinioSecretKey, ""),
		Secure: sfspCleanMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to SFSP Clean Minio: %v", err)
	}
	logger.Info("Successfully connected to SFSP Clean Minio!")

	videoMinioEndpoint := os.Getenv("VIDEO_MINIO_ENDPOINT")
	videoMinioAccessKey := os.Getenv("VIDEO_MINIO_ACCESS_KEY_ID")
	videoMinioSecretKey := os.Getenv("VIDEO_MINIO_SECRET_ACCESS_KEY")
	videoMinioUseSSL := os.Getenv("VIDEO_MINIO_USE_SSL") == "true"

	if videoMinioEndpoint == "" || videoMinioAccessKey == "" || videoMinioSecretKey == "" {
		logger.Fatalf("FATAL: Configuration for Video MinIO is incomplete. VIDEO_MINIO_ENDPOINT, VIDEO_MINIO_ACCESS_KEY_ID, and VIDEO_MINIO_SECRET_ACCESS_KEY must all be set.")
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
	if n, err := queue.RecoverProcessing(ctx, queue.StreamCompletionQueue); err != nil {
		logger.Errorf("Failed to recover unacknowledged events from %s: %v", queue.ProcessingQueueName(queue.StreamCompletionQueue), err)
	} else if n > 0 {
		logger.Warnf("Recovered %d unacknowledged event(s) back to %s", n, queue.StreamCompletionQueue)
	}

	go videoWorker(ctx)

	r := gin.Default()
	r.MaxMultipartMemory = 32 << 20

	api := r.Group("/api")
	api.Use(authMiddleware())
	{
		api.POST("/videos/upload", uploadHandler)
		api.DELETE("/videos/:id", deleteHandler)
		api.DELETE("/videos/:id/thumbnail", deleteThumbnailHandler)
	}

	logger.Info("INFO: Video Upload service starting on port 8080")
	if err := r.Run(":8080"); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

func uploadHandler(c *gin.Context) {
	uploaderIDStr := c.GetString("userID")
	if uploaderIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user session"})
		return
	}

	uploaderUUID, err := uuid.Parse(uploaderIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid uploader UUID format"})
		return
	}

	tooLarge := gin.H{"error": fmt.Sprintf("File is too large. The maximum size is %d bytes.", maxVideoUploadBytes)}
	if c.Request.ContentLength > maxVideoUploadBytes+multipartOverhead {
		c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
		return
	}

	// 本文全体を上限で打ち切る。32MB を超える部分はメモリではなく一時ファイルに置かれ、
	// ハンドラー終了時に net/http が削除する（念のため明示的にも削除する）。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoUploadBytes+multipartOverhead)
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
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

	file, header, err := c.Request.FormFile("video")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video file is required"})
		return
	}
	defer file.Close()

	if header.Size > maxVideoUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
		return
	}

	buffer := make([]byte, 512)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}

	contentType := http.DetectContentType(buffer[:n])
	if !allowedVideoMimeTypes[contentType] {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": fmt.Sprintf("Unsupported file type: %s. Only video files are allowed.", contentType)})
		return
	}

	title := c.PostForm("title")
	description := c.PostForm("description")
	teamID := c.PostForm("team_id")

	if !teamAccessCheck(c, teamID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to post to this team"})
		return
	}

	originalFilename := displayFilename(header.Filename)

	sfspRespData, sfspStatus, sfspBody, err := postFileToSFSP(c.Request.Context(), file, originalFilename, "stream")
	if err != nil {
		if sfspStatus == 0 {
			logger.Errorf("Error forwarding file to SFSP: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File scanning service is unavailable or request timed out"})
			return
		}
		logger.Errorf("SFSP upload failed: status=%d err=%v body=%s", sfspStatus, err, sfspBody)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit file for scanning"})
		return
	}

	logger.Infof(
		"SFSP response: code=%d file_id=%s job_id=%s status=%s",
		sfspStatus,
		sfspRespData.FileID,
		sfspRespData.JobID,
		sfspRespData.Status,
	)

	videoID, err := generateRandomID()
	if err != nil {
		logger.Errorf("Error generating random ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate video ID"})
		return
	}

	sfspJobID, err := uuid.Parse(sfspRespData.JobID)
	if err != nil {
		logger.Errorf(
			"Invalid Job ID from SFSP. file_id=%s job_id=%q status=%s",
			sfspRespData.FileID,
			sfspRespData.JobID,
			sfspRespData.Status,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid Job ID from SFSP"})
		return
	}

	videoStatus := StatusScanning
	processingDetails := "セキュリティスキャン待機中..."
	if sfspStatus == http.StatusOK && strings.ToLower(sfspRespData.Status) == "clean" {
		videoStatus = StatusProcessing
		processingDetails = "セキュリティスキャン完了 (既存クリーンファイル)。動画変換待機中..."
	}

	teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}
	_, err = db.Exec(context.Background(),
		"INSERT INTO videos (id, title, description, filename, uploader_id, team_id, status, sfsp_job_id, processing_details) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		videoID, title, description, originalFilename, uploaderUUID, teamIDVal, videoStatus, sfspJobID, processingDetails)
	if err != nil {
		logger.Errorf("Error creating initial video record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video record"})
		return
	}

	if videoStatus == StatusProcessing {
		fileID, err := uuid.Parse(sfspRespData.FileID)
		if err != nil {
			logger.Errorf("Invalid File ID from SFSP: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid File ID from SFSP"})
			return
		}

		event := event.ScanCompletionEvent{
			JobID:         sfspJobID,
			FileID:        fileID,
			FinalStatus:   "clean",
			ScannedAt:     time.Now().UTC(),
			SHA256:        sfspRespData.SHA256,
			Filename:      originalFilename,
			TargetService: "stream",
		}
		if err := queue.EnqueueScanCompletionEvent(context.Background(), event); err != nil {
			logger.Errorf("CRITICAL: Failed to re-publish completion event for duplicate clean file: %v", err)
		} else {
			logger.Infof("Re-published completion event for existing clean file, job %s", sfspRespData.JobID)
		}
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Video upload accepted",
		"videoID": videoID,
		"status":  videoStatus,
	})
}

// videoWorker は sfsp:completed:stream を BRPOPLPUSH で取り出し（処理中リストへ退避）、
// DB が終端状態（public / error / quarantined など）になってから Ack する。
func videoWorker(ctx context.Context) {
	logger.Info("Video Worker started. Waiting for scan completion events...")
	q := queue.StreamCompletionQueue

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
		// 変換は時間がかかるため goroutine で処理する。同時実行数は ffmpegSem で制限される。
		go handleStreamEvent(payload)
	}
}

func ackStream(payload string) {
	if err := queue.Ack(context.Background(), queue.StreamCompletionQueue, payload); err != nil {
		logger.Errorf("Failed to ack event on %s: %v", queue.StreamCompletionQueue, err)
	}
}

// findVideoByJob は sfsp_job_id に一致する動画を探す。スキャンが INSERT より先に終わる競合に備え、少し待って再試行する。
func findVideoByJob(ctx context.Context, jobID uuid.UUID) (videoID, status string, err error) {
	const attempts = 10
	for i := 1; i <= attempts; i++ {
		err = db.QueryRow(ctx, "SELECT id, status FROM videos WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", jobID).Scan(&videoID, &status)
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if i < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return
}

func handleStreamEvent(payload string) {
	ctx := context.Background()

	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		logger.Errorf("ERROR: Unmarshalling event payload (dropping): %v", err)
		ackStream(payload)
		return
	}

	if ev.TargetService != "stream" {
		logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a video event.", ev.TargetService, ev.JobID)
		ackStream(payload)
		return
	}

	videoID, status, err := findVideoByJob(ctx, ev.JobID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Errorf("[SFSP JobID: %s] Could not find a matching video record (deleted or never created). Dropping event.", ev.JobID)
			ackStream(payload)
		} else {
			// DB 障害など。Ack せず処理中リストに残し、次回起動時に回収させる。
			logger.Errorf("[SFSP JobID: %s] ERROR: Failed to look up video record: %v", ev.JobID, err)
		}
		return
	}

	if status != StatusScanning && status != StatusProcessing {
		logger.Infof("[VideoID: %s] Already in terminal status '%s'. Dropping duplicate event.", videoID, status)
		ackStream(payload)
		return
	}

	if ev.FinalStatus != "clean" {
		logger.Infof("[VideoID: %s] Scan result is '%s'. Aborting processing.", videoID, ev.FinalStatus)
		finalStatus := ev.FinalStatus
		if finalStatus == "malicious" || finalStatus == "suspicious" {
			finalStatus = StatusQuarantined
		}
		if err := updateVideoStatus(ctx, videoID, finalStatus, fmt.Sprintf("File scan failed with status: %s", ev.FinalStatus)); err == nil {
			ackStream(payload)
		}
		return
	}

	if processVideoAsync(videoID, ev.FileID.String(), ev.Filename) {
		ackStream(payload)
	}
}

// registerJob / unregisterJob / cancelJob は処理中ジョブのキャンセル登録簿を操作する。
func registerJob(videoID string, cancel context.CancelFunc) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	jobs[videoID] = cancel
}

func unregisterJob(videoID string) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	delete(jobs, videoID)
}

func cancelJob(videoID string) bool {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if cancel, ok := jobs[videoID]; ok {
		cancel()
		return true
	}
	return false
}

// videoExists は行がまだ存在するかを返す（削除済みなら false）。
func videoExists(ctx context.Context, videoID string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM videos WHERE id = $1)", videoID).Scan(&exists)
	return exists, err
}

// cleanupDeletedVideo は処理中に削除された動画について、アップロード済みの HLS とサムネイルを MinIO から消す。
func cleanupDeletedVideo(videoID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := removeHLSMinIO(ctx, videoID); err != nil {
		logger.Errorf("[VideoID: %s] Failed to clean up HLS objects of deleted video: %v", videoID, err)
	}
	if err := videoMinioClient.RemoveObject(ctx, videoBucketName, "thumbnails/"+videoID, minio.RemoveObjectOptions{}); err != nil {
		logger.Warnf("[VideoID: %s] Failed to clean up thumbnail of deleted video: %v", videoID, err)
	}
	logger.Infof("[VideoID: %s] Video was deleted during processing; removed its converted output.", videoID)
}

// processVideoAsync はクリーンな動画をダウンロードして HLS に変換し、MinIO へ展開する。
// 戻り値は「DB が終端状態になった（または動画が削除済み）ためイベントを Ack してよいか」。
func processVideoAsync(videoID, fileIDStr, filename string) bool {
	bg := context.Background()

	jobCtx, cancel := context.WithCancel(bg)
	defer cancel()
	registerJob(videoID, cancel)
	defer unregisterJob(videoID)

	// deletedOrCancelled は削除ハンドラーによるキャンセル、または行の消失を検出する。
	deletedOrCancelled := func() bool {
		if jobCtx.Err() != nil {
			return true
		}
		exists, err := videoExists(bg, videoID)
		return err == nil && !exists
	}
	// fail は動画を error にして終了する。削除済みなら後始末だけ行う。
	fail := func(details string) bool {
		if deletedOrCancelled() {
			cleanupDeletedVideo(videoID)
			return true
		}
		logger.Errorf("[VideoID: %s] Processing failed: %s", videoID, details)
		return updateVideoStatus(bg, videoID, StatusError, details) == nil
	}

	_ = updateVideoStatus(bg, videoID, StatusProcessing, "動画変換の順番待ち...")
	select {
	case ffmpegSem <- struct{}{}:
	case <-jobCtx.Done():
		cleanupDeletedVideo(videoID)
		return true
	}
	defer func() { <-ffmpegSem }()

	if deletedOrCancelled() {
		cleanupDeletedVideo(videoID)
		return true
	}

	// ユーザーのファイル名はローカルのパスに使わない。ジョブごとに一意な作業ディレクトリを作る。
	workDir, err := os.MkdirTemp(uploadDir, "job-"+videoID+"-")
	if err != nil {
		return fail(fmt.Sprintf("作業ディレクトリの作成に失敗しました: %v", err))
	}
	defer os.RemoveAll(workDir)

	tempVideoPath := filepath.Join(workDir, "input"+safeExt(filename))

	_ = updateVideoStatus(bg, videoID, StatusProcessing, "クリーンな動画ファイルをダウンロード中...")

	candidateKeys := []string{
		fileIDStr,
		fmt.Sprintf("%s/%s", fileIDStr, filename),
		fmt.Sprintf("%s%s", fileIDStr, filepath.Ext(filename)),
		fmt.Sprintf("files/%s/%s", fileIDStr, filename),
		fmt.Sprintf("files/%s", fileIDStr),
		fmt.Sprintf("uploads/%s", fileIDStr),
	}

	var downloadErr error
	downloadSuccess := false

	for attempt := 1; attempt <= 5 && !downloadSuccess; attempt++ {
		logger.Infof("[Attempt %d/5] Attempting to download clean file (fileID: %s)...", attempt, fileIDStr)

		for _, objectKey := range candidateKeys {
			downloadErr = sfspCleanMinioClient.FGetObject(jobCtx, sfspCleanBucketName, objectKey, tempVideoPath, minio.GetObjectOptions{})
			if downloadErr == nil {
				downloadSuccess = true
				logger.Infof("Successfully downloaded clean file using key: '%s'", objectKey)
				break
			}
			logger.Debugf("Failed to download with key '%s': %v", objectKey, downloadErr)
		}

		if !downloadSuccess && jobCtx.Err() == nil {
			logger.Warnf("Direct key lookup failed on attempt %d. Scanning bucket '%s' for fileID '%s'...", attempt, sfspCleanBucketName, fileIDStr)
			for obj := range sfspCleanMinioClient.ListObjects(jobCtx, sfspCleanBucketName, minio.ListObjectsOptions{Prefix: fileIDStr, Recursive: true}) {
				if obj.Err != nil {
					logger.Warnf("Error while listing objects in bucket %s: %v", sfspCleanBucketName, obj.Err)
					continue
				}
				logger.Infof("Found matching clean file key dynamically: '%s'", obj.Key)
				downloadErr = sfspCleanMinioClient.FGetObject(jobCtx, sfspCleanBucketName, obj.Key, tempVideoPath, minio.GetObjectOptions{})
				if downloadErr == nil {
					downloadSuccess = true
					break
				}
				logger.Warnf("Found matching key '%s' but download failed: %v", obj.Key, downloadErr)
			}
		}

		if !downloadSuccess && attempt < 5 {
			logger.Warnf("Clean file not found on attempt %d/5: %v. Retrying in 5 seconds...", attempt, downloadErr)
			select {
			case <-time.After(5 * time.Second):
			case <-jobCtx.Done():
			}
		}
		if jobCtx.Err() != nil {
			break
		}
	}

	if !downloadSuccess {
		return fail(fmt.Sprintf("Failed to download clean file from SFSP Clean MinIO (fileID: %s): %v", fileIDStr, downloadErr))
	}

	_ = updateVideoStatus(bg, videoID, StatusProcessing, "動画ファイルの変換を開始しました...")
	hlsOutputPath := filepath.Join(workDir, "hls")
	if err := os.MkdirAll(hlsOutputPath, os.ModePerm); err != nil {
		return fail(fmt.Sprintf("HLSディレクトリの作成に失敗しました: %v", err))
	}

	if err := convertToHLS(jobCtx, tempVideoPath, hlsOutputPath, videoID); err != nil {
		return fail(fmt.Sprintf("HLS変換に失敗しました: %v", err))
	}

	_ = updateVideoStatus(bg, videoID, StatusProcessing, "サムネイルを生成中...")
	thumbnailPath := ""
	generatedThumbnailPath := filepath.Join(workDir, "thumbnail.jpg")
	firstSegmentPath := filepath.Join(hlsOutputPath, "360p", "playlist.m3u8")

	if _, err := os.Stat(firstSegmentPath); os.IsNotExist(err) {
		err = generateThumbnail(jobCtx, tempVideoPath, generatedThumbnailPath)
	} else {
		err = generateThumbnail(jobCtx, firstSegmentPath, generatedThumbnailPath)
	}
	if err != nil {
		logger.Errorf("ERROR: Failed to generate thumbnail for video %s: %v", videoID, err)
	} else {
		thumbnailPath = uploadThumbnailToMinIO(jobCtx, videoID, generatedThumbnailPath)
	}

	// 変換したHLSをMinIOへ展開する。ローカルの作業ディレクトリは defer で削除される。
	if err := uploadHLSMinIO(jobCtx, videoID, hlsOutputPath); err != nil {
		return fail(fmt.Sprintf("HLSのMinIOアップロードに失敗しました: %v", err))
	}

	if deletedOrCancelled() {
		cleanupDeletedVideo(videoID)
		return true
	}

	m3u8RelativePath := fmt.Sprintf("/videos/%s/playlist.m3u8", videoID)
	var thumbVal interface{} = thumbnailPath
	if thumbnailPath == "" {
		thumbVal = nil
	}
	tag, err := db.Exec(bg,
		"UPDATE videos SET filename = $1, thumbnail_path = $2, status = $3, processing_details = NULL, updated_at = NOW() WHERE id = $4",
		m3u8RelativePath, thumbVal, StatusPublic, videoID)
	if err != nil {
		return fail(fmt.Sprintf("最終的なDB更新に失敗しました: %v", err))
	}
	if tag.RowsAffected() == 0 {
		// 最終更新の直前に削除された。
		cleanupDeletedVideo(videoID)
		return true
	}

	logger.Infof("INFO: Video %s processing complete. Now public.", videoID)
	return true
}

// runFFmpeg は FFMPEG_TIMEOUT を上限に ffmpeg を 1 回実行する（ctx のキャンセルでも停止する）。
func runFFmpeg(ctx context.Context, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "ffmpeg", append([]string{"-nostdin", "-y"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logger.Infof("Executing: %s", cmd.String())
	err := cmd.Run()
	if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffmpeg timed out after %s: %w", ffmpegTimeout, err)
	}
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("ffmpeg cancelled: %w", ctx.Err())
	}
	return err
}

func convertToHLS(ctx context.Context, inputPath, hlsOutputDir, videoID string) error {
	profiles := []struct {
		height       int
		videoBitrate string
		audioBitrate string
	}{
		{height: 360, videoBitrate: "800k", audioBitrate: "96k"},
		{height: 480, videoBitrate: "1200k", audioBitrate: "128k"},
		{height: 720, videoBitrate: "2000k", audioBitrate: "128k"},
		{height: 1080, videoBitrate: "4000k", audioBitrate: "192k"},
	}

	for _, p := range profiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = updateVideoStatus(context.Background(), videoID, StatusProcessing, fmt.Sprintf("%dpストリームを生成中...", p.height))
		variantDir := fmt.Sprintf("%dp", p.height)
		variantPath := filepath.Join(hlsOutputDir, variantDir)
		if err := os.MkdirAll(variantPath, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create variant directory %s: %w", variantPath, err)
		}

		playlistPath := filepath.Join(variantPath, "playlist.m3u8")
		segmentPath := filepath.Join(variantPath, "segment%03d.ts")

		ffmpegArgs := []string{
			"-i", inputPath,
			"-preset", "fast",
			"-g", "48",
			"-keyint_min", "48",
			"-sc_threshold", "0",
			"-vf", fmt.Sprintf("scale=-2:%d", p.height),
			"-c:v", "libx264",
			"-profile:v", "main",
			"-b:v", p.videoBitrate,
			"-maxrate", p.videoBitrate,
			"-bufsize", fmt.Sprintf("%dk", p.height*3),
			"-c:a", "aac",
			"-b:a", p.audioBitrate,
			"-hls_time", "10",
			"-hls_playlist_type", "vod",
			"-hls_segment_filename", segmentPath,
			playlistPath,
		}

		if err := runFFmpeg(ctx, ffmpegArgs...); err != nil {
			return fmt.Errorf("ffmpeg failed for %dp: %w", p.height, err)
		}
	}

	_ = updateVideoStatus(context.Background(), videoID, StatusProcessing, "マスタープレイリストを生成中...")
	masterPlaylistPath := filepath.Join(hlsOutputDir, "playlist.m3u8")
	masterPlaylistContent := "#EXTM3U\n#EXT-X-VERSION:3\n"
	for _, p := range profiles {
		bandwidth := 0
		if val, err := strconv.Atoi(strings.TrimSuffix(p.videoBitrate, "k")); err == nil {
			bandwidth += val * 1000
		}
		if val, err := strconv.Atoi(strings.TrimSuffix(p.audioBitrate, "k")); err == nil {
			bandwidth += val * 1000
		}

		variantDir := fmt.Sprintf("%dp", p.height)
		resolution := fmt.Sprintf("%dx%d", (p.height*16)/9, p.height)
		masterPlaylistContent += fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s\n%s/playlist.m3u8\n", bandwidth, resolution, variantDir)
	}

	if err := os.WriteFile(masterPlaylistPath, []byte(masterPlaylistContent), 0644); err != nil {
		return fmt.Errorf("failed to write master playlist: %w", err)
	}

	logger.Info("Successfully generated all HLS streams and master playlist.")
	return nil
}

func updateVideoStatus(ctx context.Context, videoID, status, details string) error {
	_, err := db.Exec(ctx, "UPDATE videos SET status = $1, processing_details = $2, updated_at = NOW() WHERE id = $3", status, details, videoID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update video status for %s: %v", videoID, err)
	}
	return err
}

func deleteHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	var uploaderUUID uuid.UUID
	var thumbnailPath sql.NullString
	err := db.QueryRow(context.Background(), "SELECT uploader_id, thumbnail_path FROM videos WHERE id = $1", videoID).Scan(&uploaderUUID, &thumbnailPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	if userIDStr != uploaderUUID.String() && !isAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this video"})
		return
	}

	// 変換中なら止める（ジョブ側は行が消えたことを確認して、アップロード済みのオブジェクトを片付ける）。
	if cancelJob(videoID) {
		logger.Infof("[VideoID: %s] Cancelled in-progress conversion job for deletion", videoID)
	}

	// 先に行を消し、変換ジョブが後から public に更新できないようにする。
	if _, err = db.Exec(context.Background(), "DELETE FROM videos WHERE id = $1", videoID); err != nil {
		logger.Errorf("Error deleting video record %s from DB: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete video record"})
		return
	}

	// HLSはMinIOへ保存しているのでMinIOから削除する。
	if err := removeHLSMinIO(context.Background(), videoID); err != nil {
		logger.Errorf("Failed to delete HLS from MinIO for video %s: %v", videoID, err)
	}
	// 旧実装の作業ディレクトリ（UPLOAD_DIR/<id>）が残っていれば削除する。
	if err := os.RemoveAll(filepath.Join(uploadDir, videoID)); err != nil {
		logger.Warnf("Failed to remove leftover local HLS directory for video %s: %v", videoID, err)
	}

	objectName := "thumbnails/" + videoID
	if thumbnailPath.Valid && thumbnailPath.String != "" {
		objectName = strings.TrimPrefix(thumbnailPath.String, "/videos/")
	}
	if err := videoMinioClient.RemoveObject(context.Background(), videoBucketName, objectName, minio.RemoveObjectOptions{}); err != nil {
		logger.Errorf("Failed to delete thumbnail from MinIO %s: %v", objectName, err)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Video deleted successfully"})
}

func deleteThumbnailHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	var uploaderUUID uuid.UUID
	var thumbnailPath sql.NullString
	err := db.QueryRow(context.Background(), "SELECT uploader_id, thumbnail_path FROM videos WHERE id = $1", videoID).Scan(&uploaderUUID, &thumbnailPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	if userIDStr != uploaderUUID.String() && !isAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this thumbnail"})
		return
	}

	if thumbnailPath.Valid && thumbnailPath.String != "" {
		objectName := strings.TrimPrefix(thumbnailPath.String, "/videos/")
		if err := videoMinioClient.RemoveObject(context.Background(), videoBucketName, objectName, minio.RemoveObjectOptions{}); err != nil {
			logger.Errorf("Failed to delete thumbnail from MinIO %s: %v", objectName, err)
		}
	}

	_, err = db.Exec(context.Background(), "UPDATE videos SET thumbnail_path = NULL, updated_at = NOW() WHERE id = $1", videoID)
	if err != nil {
		logger.Errorf("Failed to update thumbnail_path in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Thumbnail deleted successfully"})
}

// authMiddleware は JWT（HS256 固定）を検証し、ログアウト済み（Redis の blocklist:<jti>）のトークンを拒否する。
// Redis に問い合わせられない場合は拒否する（fail closed）。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		jti, _ := claims["jti"].(string)
		if jti == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		exists, err := queue.RedisClient.Exists(c.Request.Context(), "blocklist:"+jti).Result()
		if err != nil {
			logger.Errorf("Redis error checking blocklist: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
			return
		}
		if exists > 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
			return
		}

		var userID string
		for _, key := range []string{"sub", "userID", "user_id"} {
			if val, ok := claims[key].(string); ok && val != "" {
				userID = val
				break
			}
		}
		if userID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
			return
		}

		isAdmin, _ := claims["isAdmin"].(bool)
		c.Set("userID", userID)
		c.Set("isAdmin", isAdmin)
		c.Next()
	}
}

func generateThumbnail(ctx context.Context, videoPath, thumbnailPath string) error {
	return runFFmpeg(ctx, "-i", videoPath, "-ss", "00:00:01.000", "-vframes", "1", "-q:v", "2", "-vf", "scale=320:-1", thumbnailPath)
}

// uploadThumbnailToMinIO uploads a locally generated thumbnail to the video
// MinIO bucket at thumbnails/<videoID> and returns the served URL
// (/videos/thumbnails/<videoID>). It returns an empty string on failure.
func uploadThumbnailToMinIO(ctx context.Context, videoID, localPath string) string {
	f, err := os.Open(localPath)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to open thumbnail for upload: %v", videoID, err)
		return ""
	}
	defer f.Close()

	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to reset thumbnail reader: %v", videoID, err)
		return ""
	}
	stat, err := f.Stat()
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to stat thumbnail: %v", videoID, err)
		return ""
	}

	thumbnailObjectName := fmt.Sprintf("thumbnails/%s", videoID)
	err = putObjectWithRetry(ctx, thumbnailObjectName, func() (minio.UploadInfo, error) {
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return minio.UploadInfo{}, seekErr
		}
		return videoMinioClient.PutObject(ctx, videoBucketName, thumbnailObjectName, f, stat.Size(), minio.PutObjectOptions{ContentType: http.DetectContentType(buffer[:n])})
	})
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to upload thumbnail to MinIO: %v", videoID, err)
		return ""
	}
	return "/videos/" + thumbnailObjectName
}

// contentTypeForHLS returns the MIME type for HLS assets so MinIO serves them
// with the correct Content-Type for browser playback.
func contentTypeForHLS(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}

// putObjectWithRetry uploads an object to MinIO, retrying on transient failures
// such as a startup race where video-storage's MinIO is not yet ready to
// authenticate requests when video-upload-api first starts. The S3 API and
// credentials are correct, so a few attempts with backoff resolves the race
// without masking a genuine misconfiguration.
func putObjectWithRetry(ctx context.Context, objectName string, upload func() (minio.UploadInfo, error)) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, err := upload()
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		if attempt < maxAttempts {
			logger.Warnf("MinIO upload attempt %d/%d failed for %s: %v", attempt, maxAttempts, objectName, err)
			select {
			case <-time.After(time.Duration(attempt*2) * time.Second):
			case <-ctx.Done():
			}
		}
	}
	return fmt.Errorf("failed to upload %s to MinIO: %w", objectName, lastErr)
}

// uploadHLSMinIO uploads every file under localHlsDir to the video MinIO bucket.
// Object keys are "<videoID>/<rel>" (no bucket prefix), matching the nginx
// stream route which consumes "/videos/<videoID>/<path>" as bucket=videos.
func uploadHLSMinIO(ctx context.Context, videoID, localHlsDir string) error {
	return filepath.Walk(localHlsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localHlsDir, path)
		if err != nil {
			return err
		}
		objectName := fmt.Sprintf("%s/%s", videoID, filepath.ToSlash(rel))
		logger.Infof("[VideoID: %s] Uploading HLS object %s to MinIO", videoID, objectName)
		f, openErr := os.Open(path)
		if openErr != nil {
			return fmt.Errorf("failed to open %s: %w", objectName, openErr)
		}
		defer f.Close()
		stat, statErr := f.Stat()
		if statErr != nil {
			return fmt.Errorf("failed to stat %s: %w", objectName, statErr)
		}
		if putErr := putObjectWithRetry(ctx, objectName, func() (minio.UploadInfo, error) {
			if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
				return minio.UploadInfo{}, seekErr
			}
			return videoMinioClient.PutObject(ctx, videoBucketName, objectName, f, stat.Size(), minio.PutObjectOptions{ContentType: contentTypeForHLS(path)})
		}); putErr != nil {
			return fmt.Errorf("failed to upload %s to MinIO: %w", objectName, putErr)
		}
		return nil
	})
}

// removeHLSMinIO deletes all objects under "<videoID>/" from the video MinIO bucket.
func removeHLSMinIO(ctx context.Context, videoID string) error {
	objectCh := videoMinioClient.ListObjects(ctx, videoBucketName, minio.ListObjectsOptions{
		Prefix:    fmt.Sprintf("%s/", videoID),
		Recursive: true,
	})
	var firstErr error
	for obj := range objectCh {
		if obj.Err != nil {
			logger.Warnf("[VideoID: %s] Error listing object %s: %v", videoID, obj.Key, obj.Err)
			if firstErr == nil {
				firstErr = obj.Err
			}
			continue
		}
		if err := videoMinioClient.RemoveObject(ctx, videoBucketName, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			logger.Errorf("[VideoID: %s] Failed to remove object %s: %v", videoID, obj.Key, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
