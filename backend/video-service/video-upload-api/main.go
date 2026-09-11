package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
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
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db                    *pgxpool.Pool
	jwtSecret             []byte
	uploadDir             string
	thumbnailDir          string
	sfspApiUrl            string
	sfspCleanMinioClient  *minio.Client
	sfspCleanBucketName   string
	maxUploadSize         = int64(2 * 1024 * 1024 * 1024) // 2GB
	logger                *zap.SugaredLogger
	allowedVideoMimeTypes = map[string]bool{
		"video/mp4":        true,
		"video/webm":       true,
		"video/quicktime":  true,
		"video/x-msvideo":  true,
		"video/x-matroska": true,
	}
)

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
	checkQuery := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = '%s')", targetDB)
	if err := adminDB.QueryRow(checkQuery).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check database existence: %w", err)
	}

	if !exists {
		logger.Infof("Database '%s' does not exist. Creating...", targetDB)
		_, err = adminDB.Exec(fmt.Sprintf("CREATE DATABASE \"%s\"", targetDB))
		if err != nil {
			return fmt.Errorf("failed to create database '%s': %w", targetDB, err)
		}
		logger.Infof("Database '%s' created successfully.", targetDB)
	}

	return nil
}

func initTables(ctx context.Context, pool *pgxpool.Pool) error {
	createVideosTableSQL := `
    CREATE TABLE IF NOT EXISTS videos (
       id VARCHAR(255) PRIMARY KEY,
       title VARCHAR(255) NOT NULL,
       description TEXT,
       filename VARCHAR(255),
       thumbnail_path VARCHAR(255),
       uploader_id UUID NOT NULL,
       status VARCHAR(50) NOT NULL,
       sfsp_job_id UUID,
       processing_details TEXT,
       created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
    );`

	_, err := pool.Exec(ctx, createVideosTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create videos table: %w", err)
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
	uploadDir = os.Getenv("UPLOAD_DIR")
	thumbnailDir = os.Getenv("THUMBNAIL_DIR")
	databaseUrl := os.Getenv("DATABASE_URL")
	sfspApiUrl = os.Getenv("SFSP_API_URL")

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

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)
	file, header, err := c.Request.FormFile("video")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video file is required"})
		return
	}
	defer file.Close()

	buffer := make([]byte, 512)
	_, err = file.Read(buffer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}

	contentType := http.DetectContentType(buffer)
	if !allowedVideoMimeTypes[contentType] {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": fmt.Sprintf("Unsupported file type: %s. Only video files are allowed.", contentType)})
		return
	}

	title := c.PostForm("title")
	description := c.PostForm("description")

	tmpFile, err := os.CreateTemp("", "sfsp-upload-*.tmp")
	if err != nil {
		logger.Errorf("Failed to create temp file for SFSP upload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process upload file"})
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	writer := multipart.NewWriter(tmpFile)

	if err := writer.WriteField("target_service", "stream"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write target_service field for SFSP"})
		return
	}

	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create form for SFSP"})
		return
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}

	_, err = io.Copy(part, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to copy file for SFSP"})
		return
	}
	writer.Close()

	fi, err := tmpFile.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to stat temp upload file"})
		return
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rewind temp file"})
		return
	}

	sfspRequest, err := http.NewRequestWithContext(c.Request.Context(), "POST", sfspApiUrl+"/api/v1/files", tmpFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create SFSP request"})
		return
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())
	sfspRequest.ContentLength = fi.Size()

	client := &http.Client{Timeout: 10 * time.Minute}
	sfspResponse, err := client.Do(sfspRequest)
	if err != nil {
		logger.Errorf("Error forwarding file to SFSP: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File scanning service is unavailable or request timed out"})
		return
	}
	defer sfspResponse.Body.Close()

	respBytes, err := io.ReadAll(sfspResponse.Body)
	if err != nil {
		logger.Errorf("Failed to read SFSP response body: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response from scanning service"})
		return
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		logger.Errorf("SFSP returned non-2xx status: %d, body: %s", sfspResponse.StatusCode, string(respBytes))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit file for scanning", "details": string(respBytes)})
		return
	}

	var sfspRespData SfspUploadResponse
	if err := json.Unmarshal(respBytes, &sfspRespData); err != nil {
		logger.Errorf("Failed to decode SFSP response JSON: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode SFSP response"})
		return
	}

	logger.Infof(
		"SFSP response: code=%d file_id=%s job_id=%s status=%s",
		sfspResponse.StatusCode,
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
	if sfspResponse.StatusCode == http.StatusOK && strings.ToLower(sfspRespData.Status) == "clean" {
		videoStatus = StatusProcessing
		processingDetails = "セキュリティスキャン完了 (既存クリーンファイル)。動画変換待機中..."
	}

	_, err = db.Exec(context.Background(),
		"INSERT INTO videos (id, title, description, filename, uploader_id, status, sfsp_job_id, processing_details) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
		videoID, title, description, header.Filename, uploaderUUID, videoStatus, sfspJobID, processingDetails)
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
			Filename:      header.Filename,
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

func videoWorker(ctx context.Context) {
	logger.Info("Video Worker started. Waiting for scan completion events...")
	streamCompletionQueue := queue.StreamCompletionQueue

	for {
		result, err := queue.RedisClient.BRPop(ctx, 0, streamCompletionQueue).Result()
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

		if event.TargetService != "stream" {
			logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a video event.", event.TargetService, event.JobID)
			continue
		}

		var videoID, title, description string
		var uploaderID uuid.UUID
		err = db.QueryRow(ctx, "SELECT id, title, description, uploader_id FROM videos WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", event.JobID).Scan(&videoID, &title, &description, &uploaderID)
		if err != nil {
			logger.Errorf("[SFSP JobID: %s] ERROR: Could not find a matching video record in app-db: %v", event.JobID, err)
			continue
		}

		if event.FinalStatus != "clean" {
			logger.Infof("[VideoID: %s] Scan result is '%s'. Aborting processing.", videoID, event.FinalStatus)
			finalStatus := event.FinalStatus
			if finalStatus == "malicious" || finalStatus == "suspicious" {
				finalStatus = StatusQuarantined
			}
			updateVideoStatus(ctx, videoID, finalStatus, fmt.Sprintf("File scan failed with status: %s", event.FinalStatus))
			continue
		}

		go processVideoAsync(ctx, videoID, event.FileID.String(), event.Filename, title, description, uploaderID.String())
	}
}

func processVideoAsync(ctx context.Context, videoID, fileIDStr, filename, title, description string, uploaderID string) {
	tempVideoPath := filepath.Join(uploadDir, filename)
	defer os.Remove(tempVideoPath)

	updateVideoStatus(ctx, videoID, StatusProcessing, "クリーンな動画ファイルをダウンロード中...")

	ext := filepath.Ext(filename)

	candidateKeys := []string{
		fmt.Sprintf("%s/%s", fileIDStr, filename),
		fileIDStr,
		fmt.Sprintf("%s%s", fileIDStr, ext),
		fmt.Sprintf("files/%s/%s", fileIDStr, filename),
		fmt.Sprintf("files/%s", fileIDStr),
		fmt.Sprintf("uploads/%s", fileIDStr),
		filename,
	}

	var downloadErr error
	downloadSuccess := false

	for attempt := 1; attempt <= 5; attempt++ {
		logger.Infof("[Attempt %d/5] Attempting to download clean file (fileID: %s)...", attempt, fileIDStr)

		for _, objectKey := range candidateKeys {
			logger.Infof("Downloading clean file using key: '%s' from bucket: '%s'...", objectKey, sfspCleanBucketName)
			downloadErr = sfspCleanMinioClient.FGetObject(ctx, sfspCleanBucketName, objectKey, tempVideoPath, minio.GetObjectOptions{})
			if downloadErr == nil {
				downloadSuccess = true
				logger.Infof("Successfully downloaded clean file using key: '%s'", objectKey)
				break
			} else {
				logger.Debugf("Failed to download with key '%s': %v", objectKey, downloadErr)
			}
		}

		if !downloadSuccess {
			logger.Warnf("Direct key lookup failed on attempt %d. Scanning bucket '%s' for fileID '%s'...", attempt, sfspCleanBucketName, fileIDStr)
			objectCh := sfspCleanMinioClient.ListObjects(ctx, sfspCleanBucketName, minio.ListObjectsOptions{Recursive: true})
			foundDuringScan := false

			for obj := range objectCh {
				if obj.Err != nil {
					logger.Warnf("Error while listing objects in bucket %s: %v", sfspCleanBucketName, obj.Err)
					continue
				}
				logger.Infof("[DIAGNOSTIC] Bucket Scan found object: Key='%s', Size=%d", obj.Key, obj.Size)

				if strings.Contains(obj.Key, fileIDStr) {
					foundDuringScan = true
					logger.Infof("Found matching clean file key dynamically: '%s'", obj.Key)
					downloadErr = sfspCleanMinioClient.FGetObject(ctx, sfspCleanBucketName, obj.Key, tempVideoPath, minio.GetObjectOptions{})
					if downloadErr == nil {
						downloadSuccess = true
						break
					} else {
						logger.Warnf("Found matching key '%s' but download failed: %v", obj.Key, downloadErr)
					}
				}
			}
			if !foundDuringScan && !downloadSuccess {
				logger.Warnf("Bucket scan completed, but no object key containing the fileID '%s' was found.", fileIDStr)
			}
		}

		if downloadSuccess {
			break
		}

		if attempt < 5 {
			logger.Warnf("Clean file not found on attempt %d/5: %v. Retrying in 5 seconds...", attempt, downloadErr)
			time.Sleep(5 * time.Second)
		}
	}

	if !downloadSuccess {
		updateVideoStatus(ctx, videoID, StatusError, fmt.Sprintf("Failed to download clean file from SFSP Clean MinIO (fileID: %s): %v", fileIDStr, downloadErr))
		return
	}

	updateVideoStatus(ctx, videoID, StatusProcessing, "動画ファイルの変換を開始しました...")
	hlsOutputPath := filepath.Join(uploadDir, videoID)
	if err := os.MkdirAll(hlsOutputPath, os.ModePerm); err != nil {
		updateVideoStatus(ctx, videoID, StatusError, fmt.Sprintf("HLSディレクトリの作成に失敗しました: %v", err))
		return
	}

	err := convertToHLS(tempVideoPath, hlsOutputPath, videoID)
	if err != nil {
		updateVideoStatus(ctx, videoID, StatusError, fmt.Sprintf("HLS変換に失敗しました: %v", err))
		return
	}

	updateVideoStatus(ctx, videoID, StatusProcessing, "サムネイルを生成中...")
	thumbnailPath := ""
	generatedThumbnailFilename := fmt.Sprintf("%s.jpg", videoID)
	generatedThumbnailPath := filepath.Join(thumbnailDir, generatedThumbnailFilename)
	firstSegmentPath := filepath.Join(hlsOutputPath, "360p", "playlist.m3u8")

	if _, err := os.Stat(firstSegmentPath); os.IsNotExist(err) {
		err = generateThumbnail(ctx, tempVideoPath, generatedThumbnailPath)
	} else {
		err = generateThumbnail(ctx, firstSegmentPath, generatedThumbnailPath)
	}

	if err != nil {
		logger.Errorf("ERROR: Failed to generate thumbnail for video %s: %v", videoID, err)
	} else {
		thumbnailPath = "/storage/thumbnails/" + generatedThumbnailFilename
	}

	updateVideoStatus(ctx, videoID, StatusProcessing, "データベースを更新中...")
	m3u8RelativePath := fmt.Sprintf("/storage/videos/%s/playlist.m3u8", videoID)
	_, err = db.Exec(ctx,
		"UPDATE videos SET filename = $1, thumbnail_path = $2, status = $3, processing_details = NULL WHERE id = $4",
		m3u8RelativePath, thumbnailPath, StatusPublic, videoID)
	if err != nil {
		updateVideoStatus(ctx, videoID, StatusError, fmt.Sprintf("最終的なDB更新に失敗しました: %v", err))
		return
	}

	logger.Infof("INFO: Video %s processing complete. Now public.", videoID)
}

func convertToHLS(inputPath, hlsOutputDir, videoID string) error {
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
		updateVideoStatus(context.Background(), videoID, StatusProcessing, fmt.Sprintf("%dpストリームを生成中...", p.height))
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

		cmd := exec.Command("ffmpeg", ffmpegArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		logger.Infof("Executing ffmpeg for %dp: %s", p.height, cmd.String())
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ffmpeg failed for %dp: %w", p.height, err)
		}
	}

	updateVideoStatus(context.Background(), videoID, StatusProcessing, "マスタープレイリストを生成中...")
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

	err := os.WriteFile(masterPlaylistPath, []byte(masterPlaylistContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to write master playlist: %w", err)
	}

	logger.Info("Successfully generated all HLS streams and master playlist.")
	return nil
}

func updateVideoStatus(ctx context.Context, videoID, status, details string) {
	_, err := db.Exec(ctx, "UPDATE videos SET status = $1, processing_details = $2 WHERE id = $3", status, details, videoID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update video status for %s: %v", videoID, err)
	}
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
	err := db.QueryRow(context.Background(), "SELECT uploader_id FROM videos WHERE id = $1", videoID).Scan(&uploaderUUID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	if userIDStr != uploaderUUID.String() && !isAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this video"})
		return
	}

	hlsPath := filepath.Join(uploadDir, videoID)
	if err := os.RemoveAll(hlsPath); err != nil {
		logger.Errorf("Failed to delete HLS directory %s: %v", hlsPath, err)
	}

	var thumbnailPath string
	_ = db.QueryRow(context.Background(), "SELECT thumbnail_path FROM videos WHERE id = $1", videoID).Scan(&thumbnailPath)
	if thumbnailPath != "" {
		thumbFilename := filepath.Base(thumbnailPath)
		thumbPath := filepath.Join(thumbnailDir, thumbFilename)
		if err := os.Remove(thumbPath); err != nil {
			logger.Errorf("Failed to delete thumbnail file %s: %v", thumbPath, err)
		}
	}

	_, err = db.Exec(context.Background(), "DELETE FROM videos WHERE id = $1", videoID)
	if err != nil {
		logger.Errorf("Error deleting video record %s from DB: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete video record"})
		return
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
		thumbFilename := filepath.Base(thumbnailPath.String)
		thumbPath := filepath.Join(thumbnailDir, thumbFilename)
		if err := os.Remove(thumbPath); err != nil {
			logger.Errorf("Failed to delete thumbnail file %s: %v", thumbPath, err)
		}
	}

	_, err = db.Exec(context.Background(), "UPDATE videos SET thumbnail_path = NULL WHERE id = $1", videoID)
	if err != nil {
		logger.Errorf("Failed to update thumbnail_path in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Thumbnail deleted successfully"})
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
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			var userID string
			if val, ok := claims["userID"].(string); ok {
				userID = val
			} else if val, ok := claims["user_id"].(string); ok {
				userID = val
			}

			if userID == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
				return
			}

			isAdmin, ok := claims["isAdmin"].(bool)
			if !ok {
				isAdmin = false
			}
			c.Set("userID", userID)
			c.Set("isAdmin", isAdmin)
			c.Next()
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		}
	}
}

func generateThumbnail(ctx context.Context, videoPath, thumbnailPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-i", videoPath, "-ss", "00:00:01.000", "-vframes", "1", "-q:v", "2", "-vf", "scale=320:-1", thumbnailPath)
	return cmd.Run()
}