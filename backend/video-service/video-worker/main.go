package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
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
	sfspCleanMinioClient *minio.Client
	sfspCleanBucketName  = "clean-files"
	videoMinioClient     *minio.Client
	videoBucketName      = "videos"
	logger               *zap.SugaredLogger
)

type Video struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Filename          string    `json:"filename"`
	ThumbnailPath     string    `json:"thumbnail_path"`
	UploaderID        string    `json:"uploader_id"`
	UploaderName      string    `json:"uploader_name"`
	TeamID            string    `json:"team_id"`
	Status            string      `json:"status"`
	ProcessingDetails string      `json:"processing_details"`
	ThumbnailSfspJobID *uuid.UUID  `json:"thumbnail_sfsp_job_id,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
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

	sfspApiUrl = os.Getenv("SFSP_API_URL")
	if sfspApiUrl == "" {
		logger.Fatal("SFSP_API_URL environment variable is not set")
	}

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

	go videoThumbnailWorker(ctx)

	r := gin.Default()
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
		api.PUT("/:id", updateVideoHandler) // 👈 PUTハンドラーを追加
	}

	logger.Info("Stream service (metadata) starting on port 8081")
	if err := r.Run(":8081"); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

// 動画一覧・検索
func listVideosHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	teamToken := c.Query("team")

	// team パラメータ: ある場合はそのチーム限定(team_id = $)、なければ公開(team_id IS NULL)のみ。
	// q パラメータでタイトル検索。プレースホルダーは args の順序に合わせて番号を振る。
	whereConds := []string{"1=1"}
	var args []interface{}
	argIdx := 1
	if teamToken != "" {
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

	var rows pgx.Rows
	var err error
	// 💡 COALESCE(description, '') で NULL 対策を追加
	rows, err = db.Query(context.Background(),
		`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, status, COALESCE(processing_details, '') as processing_details, created_at
         FROM videos
         `+whereClause+`
         ORDER BY created_at DESC LIMIT 50`, args...)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database query failed"})
		return
	}
	defer rows.Close()
	videos := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.Status, &v.ProcessingDetails, &v.CreatedAt); err != nil {
			log.Printf("Error scanning video row: %v", err)
			continue
		}
		videos = append(videos, v)
	}
	c.JSON(http.StatusOK, videos)
}

// 動画詳細取得
func videoDetailsHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	var v Video
	// 💡 COALESCE(description, '') で NULL 対策を追加
	err := db.QueryRow(context.Background(),
		`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, status, COALESCE(processing_details, '') as processing_details, thumbnail_sfsp_job_id, created_at
        FROM videos
        WHERE id = $1`, videoID).Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.Status, &v.ProcessingDetails, &v.ThumbnailSfspJobID, &v.CreatedAt)
	if err != nil {
		log.Printf("ERROR: Database query failed in videoDetailsHandler for ID %s: %v", videoID, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}
	c.JSON(http.StatusOK, v)
}

// 💡 動画更新（PUT /api/videos/:id）
// title/description を multipart から受取り、thumbnail が存在すれば SFSP スキャンへ投入する。
func updateVideoHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	title := c.Request.FormValue("title")
	description := c.Request.FormValue("description")

	_, err := db.Exec(c.Request.Context(),
		"UPDATE videos SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
		title, description, videoID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update video ID %s: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video"})
		return
	}

	file, _, err := c.Request.FormFile("thumbnail")
	if err != nil {
		// サムネイルなし（タイトル/説明のみ更新）
		c.JSON(http.StatusOK, gin.H{"message": "Video updated successfully"})
		return
	}
	defer file.Close()

	tmpFile, err := os.CreateTemp("", "sfsp-video-thumb-*.tmp")
	if err != nil {
		logger.Errorf("Failed to create temp file for SFSP upload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process upload file"})
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, file); err != nil {
		logger.Errorf("Failed to copy thumbnail to temp file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process upload file"})
		return
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("Failed to reset temp file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process upload file"})
		return
	}

	var sfspResp SfspUploadResponse
	if err := uploadToSFSP(c.Request.Context(), tmpFile, "video-thumbnail", &sfspResp); err != nil {
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

// uploadToSFSP はファイルを SFSP へ multipart 投稿し、応答を resp に格納する。
func uploadToSFSP(ctx context.Context, tmpFile *os.File, targetService string, resp *SfspUploadResponse) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("target_service", targetService); err != nil {
		return fmt.Errorf("failed to write target_service: %w", err)
	}

	part, err := writer.CreateFormFile("file", "thumbnail.jpg")
	if err != nil {
		return fmt.Errorf("failed to create form: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to reset file reader: %w", err)
	}

	if _, err := io.Copy(part, tmpFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}
	writer.Close()

	sfspRequest, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", body)
	if err != nil {
		return fmt.Errorf("failed to create SFSP request: %w", err)
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 10 * time.Minute}
	sfspResponse, err := client.Do(sfspRequest)
	if err != nil {
		return fmt.Errorf("SFSP request failed: %w", err)
	}
	defer sfspResponse.Body.Close()

	respBytes, err := io.ReadAll(sfspResponse.Body)
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

// videoThumbnailWorker は SFSP からのサムネイル完了イベントを待ち、クリーンなサムネイルを
// video-storage へ展開した後、videos.thumbnail_path を更新し thumbnail_sfsp_job_id を消去する。
func videoThumbnailWorker(ctx context.Context) {
	logger.Info("Video Thumbnail Worker started. Waiting for thumbnail completion events...")
	thumbnailCompletionQueue := queue.VideoThumbnailCompletionQueue

	for {
		result, err := queue.RedisClient.BRPop(ctx, 0, thumbnailCompletionQueue).Result()
		if err != nil {
			logger.Errorf("Error fetching event from Redis: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if len(result) < 2 {
			continue
		}

		var event event.ScanCompletionEvent
		if err := json.Unmarshal([]byte(result[1]), &event); err != nil {
			logger.Errorf("ERROR: Unmarshalling event payload: %v", err)
			continue
		}

		if event.TargetService != "video-thumbnail" {
			logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a video thumbnail event.", event.TargetService, event.JobID)
			continue
		}

		var videoID string
		var currentThumbnailPath string
		err = db.QueryRow(ctx, "SELECT id, COALESCE(thumbnail_path, '') FROM videos WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", event.JobID).Scan(&videoID, &currentThumbnailPath)
		if err != nil {
			logger.Errorf("[Thumbnail JobID: %s] ERROR: Could not find a matching video record: %v", event.JobID, err)
			continue
		}

		if event.FinalStatus != "clean" {
			logger.Infof("[VideoID: %s] Thumbnail scan result is '%s'. Aborting thumbnail processing.", videoID, event.FinalStatus)
			_, err = db.Exec(ctx, "UPDATE videos SET thumbnail_sfsp_job_id = NULL WHERE id = $1", videoID)
			if err != nil {
				logger.Errorf("[VideoID: %s] Failed to clear thumbnail scan job: %v", videoID, err)
			}
			continue
		}

		logger.Infof("[VideoID: %s] Thumbnail is clean. Processing thumbnail...", videoID)

		fileIDStr := event.FileID.String()
		candidateKeys := []string{
			fileIDStr,
			fmt.Sprintf("%s/%s", fileIDStr, event.Filename),
		}
		if event.SHA256 != "" {
			candidateKeys = append(candidateKeys, fmt.Sprintf("%s/%s", event.SHA256, event.Filename))
		}
		candidateKeys = append(candidateKeys, event.Filename)

		tempThumbPath := filepath.Join(os.TempDir(), fmt.Sprintf("video-thumb-%s-%s", videoID, event.Filename))
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
				logger.Infof("[VideoID: %s] Trying thumbnail download key '%s' from SFSP MinIO bucket '%s'...", videoID, objectKey, sfspCleanBucketName)
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
			os.Remove(tempThumbPath)
			continue
		}

data, err := os.ReadFile(tempThumbPath)
	os.Remove(tempThumbPath)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to read downloaded thumbnail: %v", videoID, err)
		continue
	}

	// Name the thumbnail object after the video ID so it is directly identifiable
	// with its video (mirrors videos/<videoID>/). Deterministic naming lets
	// us reclaim storage by deleting the previous thumbnail first.
	thumbnailObjectName := fmt.Sprintf("thumbnails/%s", videoID)

	// Reclaim storage: delete the previous thumbnail before uploading the new one.
	if currentThumbnailPath != "" {
		oldObjectName := strings.TrimPrefix(currentThumbnailPath, "/videos/")
		if err := videoMinioClient.RemoveObject(ctx, videoBucketName, oldObjectName, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("[VideoID: %s] WARNING: Failed to delete previous thumbnail %s: %v", videoID, oldObjectName, err)
		} else {
			logger.Infof("[VideoID: %s] Deleted previous thumbnail %s", videoID, oldObjectName)
		}
	}

	contentType := http.DetectContentType(data)
	_, err = videoMinioClient.PutObject(ctx, videoBucketName, thumbnailObjectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to upload thumbnail to MinIO: %v", videoID, err)
		continue
	}

	thumbnailURL := "/videos/" + thumbnailObjectName
	_, err = db.Exec(ctx, "UPDATE videos SET thumbnail_path = $1, thumbnail_sfsp_job_id = NULL WHERE id = $2", thumbnailURL, videoID)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to update thumbnail URL in DB: %v", videoID, err)
		continue
	}

	logger.Infof("[VideoID: %s] Thumbnail processing complete. Stored at %s", videoID, thumbnailURL)
	}
}