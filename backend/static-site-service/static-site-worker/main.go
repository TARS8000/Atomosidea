package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db              *sql.DB
	minioClient     *minio.Client
	minioBucket     string
	sfspMinioClient *minio.Client
	sfspBucketName  = "clean-files"
	logger          *zap.SugaredLogger
)

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	err = godotenv.Load()
	if err != nil {
		logger.Warnf("Error loading .env file, assuming production environment: %v", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		logger.Fatal("DATABASE_URL environment variable not set")
	}
	for i := 0; i < 5; i++ {
		db, err = sql.Open("postgres", dbURL)
		if err == nil {
			err = db.Ping()
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

	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	minioAccessKeyID := os.Getenv("MINIO_ACCESS_KEY_ID")
	minioSecretAccessKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	minioBucket = os.Getenv("MINIO_BUCKET_NAME")

	if minioEndpoint == "" || minioAccessKeyID == "" || minioSecretAccessKey == "" || minioBucket == "" {
		logger.Fatal("MinIO environment variables not set")
	}

	for i := 0; i < 5; i++ {
		minioClient, err = minio.New(minioEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(minioAccessKeyID, minioSecretAccessKey, ""),
			Secure: minioUseSSL,
		})
		if err == nil {
			_, err = minioClient.ListBuckets(context.Background())
			if err == nil {
				break
			}
		}
		logger.Warnf("Failed to connect to MinIO, retrying in 5 seconds... (%d/5)", i+1)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		logger.Fatalf("Failed to create MinIO client after multiple retries: %v", err)
	}
	logger.Info("Successfully connected to MinIO!")

	sfspMinioEndpoint := os.Getenv("SFSP_MINIO_ENDPOINT")
	sfspMinioAccessKey := os.Getenv("SFSP_MINIO_ACCESS_KEY_ID")
	sfspMinioSecretKey := os.Getenv("SFSP_MINIO_SECRET_ACCESS_KEY")
	sfspMinioUseSSL := os.Getenv("SFSP_MINIO_USE_SSL") == "true"

	sfspMinioClient, err = minio.New(sfspMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspMinioAccessKey, sfspMinioSecretKey, ""),
		Secure: sfspMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to SFSP MinIO: %v", err)
	}
	logger.Info("Successfully connected to SFSP MinIO!")

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	logger.Info("Static Site Worker started. Waiting for jobs...")

	staticSiteCompletionQueue := queue.StaticSiteCompletionQueue
	thumbnailCompletionQueue := queue.ThumbnailCompletionQueue

	for {
		result, err := queue.RedisClient.BRPop(context.Background(), 0, staticSiteCompletionQueue, thumbnailCompletionQueue).Result()
		if err != nil {
			logger.Errorf("Error popping job from Redis: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		logger.Debugf("Received Redis message: %+v", result)

		queueName := result[0]
		eventPayload := result[1]

		var event event.ScanCompletionEvent
		if err := json.Unmarshal([]byte(eventPayload), &event); err != nil {
			logger.Errorf("Error unmarshalling event payload: %v", err)
			continue
		}
		logger.Debugf("Parsed event: %+v", event)

		switch queueName {
		case staticSiteCompletionQueue:
			if event.TargetService != "static-site" {
				logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a static site event.", event.TargetService, event.JobID)
				continue
			}
			var siteID string
			logger.Debugf("Looking up site by JobID=%s", event.JobID)
			err = db.QueryRow("SELECT id FROM static_sites WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", event.JobID).Scan(&siteID)
			if err != nil {
				logger.Errorf("[SFSP JobID: %s] ERROR: Could not find a matching static_site record in app-db: %v", event.JobID, err)
				continue
			}

			if event.FinalStatus != "clean" {
				logger.Infof("[SiteID: %s] Scan result is '%s'. Aborting processing.", siteID, event.FinalStatus)
				finalStatus := event.FinalStatus
				if finalStatus == "malicious" || finalStatus == "suspicious" {
					finalStatus = "quarantined"
				}
				updateProcessingStatus(siteID, finalStatus, fmt.Sprintf("File scan failed with status: %s", event.FinalStatus))
				continue
			}

			go processStaticSiteJob(siteID, event)
		case thumbnailCompletionQueue:
			processStaticSiteThumbnail(context.Background(), &event)
		default:
			logger.Infof("INFO: Skipping event from unknown queue: %s", queueName)
		}
	}
}

func updateProcessingStatus(siteID, status, details string) {
	_, err := db.Exec("UPDATE static_sites SET status = $1, processing_details = $2 WHERE id = $3", status, details, siteID)
	if err != nil {
		logger.Errorf("Failed to update processing details for SiteID %s: %v", siteID, err)
	}
}

func processStaticSiteJob(siteID string, event event.ScanCompletionEvent) {
	logger.Debugf(
		"processStaticSiteJob started. siteID=%s jobID=%s file=%s",
		siteID,
		event.JobID,
		event.Filename,
	)

	ctx := context.Background()
	tempZipPath := filepath.Join(os.TempDir(), event.Filename)
	unzipPath := filepath.Join(os.TempDir(), fmt.Sprintf("site-%s", siteID))
	defer os.Remove(tempZipPath)
	defer os.RemoveAll(unzipPath)

	updateProcessingStatus(siteID, "processing", "クリーンなファイルをダウンロード中...")

	// --- SFSP MinIO からのダウンロード処理 ---
	fileIDStr := fmt.Sprintf("%v", event.FileID)

	var keysToTry []string
	if fileIDStr != "" && fileIDStr != "<nil>" {
		keysToTry = append(keysToTry, fileIDStr)                                        // 1. Plain FileID
		keysToTry = append(keysToTry, fmt.Sprintf("%s/%s", fileIDStr, event.Filename)) // 2. FileID/Filename
	}
	if event.SHA256 != "" {
		keysToTry = append(keysToTry, fmt.Sprintf("%s/%s", event.SHA256, event.Filename)) // 3. SHA256/Filename
		keysToTry = append(keysToTry, event.SHA256)                                       // 4. Plain SHA256
	}
	if event.Filename != "" {
		keysToTry = append(keysToTry, event.Filename)                                     // 5. Raw Filename
	}

	var downloadErr error
	var downloadedKey string
	downloadSuccess := false

	for _, sfspObjectName := range keysToTry {
		logger.Infof("[SiteID: %s] Attempting download with key: %s", siteID, sfspObjectName)
		downloadErr = sfspMinioClient.FGetObject(ctx, sfspBucketName, sfspObjectName, tempZipPath, minio.GetObjectOptions{})
		if downloadErr == nil {
			downloadSuccess = true
			downloadedKey = sfspObjectName // 成功したキーを記録
			logger.Infof("[SiteID: %s] Successfully downloaded %s from SFSP", siteID, sfspObjectName)
			break
		}
		logger.Warnf("[SiteID: %s] Failed to download with key (%s): %v", siteID, sfspObjectName, downloadErr)
	}

	if !downloadSuccess {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to download clean file from SFSP MinIO: %v", downloadErr))
		logger.Errorf("[SiteID: %s] ERROR: Download from SFSP failed after retries: %v", siteID, downloadErr)
		return
	}

	updateProcessingStatus(siteID, "processing", "ZIPファイルを解凍中...")
	zipReader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to open zip file: %v", err))
		return
	}
	defer zipReader.Close()

	var indexZipPath string
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		normalizedPath := strings.ReplaceAll(file.Name, "\\", "/")
		if strings.HasSuffix(strings.ToLower(normalizedPath), "index.html") {
			indexZipPath = normalizedPath
			break
		}
	}

	if indexZipPath == "" {
		updateProcessingStatus(siteID, "error", "index.html not found in the ZIP file")
		return
	}

	zipRootDir := path.Dir(indexZipPath)
	if zipRootDir == "." {
		zipRootDir = ""
	} else {
		zipRootDir = zipRootDir + "/"
	}

	updateProcessingStatus(siteID, "processing", "ファイルをアップロード中...")
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		normalizedPath := strings.ReplaceAll(file.Name, "\\", "/")

		relPath := normalizedPath
		if zipRootDir != "" && strings.HasPrefix(normalizedPath, zipRootDir) {
			relPath = strings.TrimPrefix(normalizedPath, zipRootDir)
		}

		rc, err := file.Open()
		if err != nil {
			updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to open file in zip: %v", err))
			return
		}

		minioPath := path.Join(siteID, relPath)

		contentType := "application/octet-stream"
		lowerPath := strings.ToLower(relPath)
		if strings.HasSuffix(lowerPath, ".html") {
			contentType = "text/html"
		} else if strings.HasSuffix(lowerPath, ".css") {
			contentType = "text/css"
		} else if strings.HasSuffix(lowerPath, ".js") {
			contentType = "application/javascript"
		} else if strings.HasSuffix(lowerPath, ".png") {
			contentType = "image/png"
		} else if strings.HasSuffix(lowerPath, ".jpg") || strings.HasSuffix(lowerPath, ".jpeg") {
			contentType = "image/jpeg"
		} else if strings.HasSuffix(lowerPath, ".svg") {
			contentType = "image/svg+xml"
		}

		_, err = minioClient.PutObject(ctx, minioBucket, minioPath, rc, file.FileInfo().Size(), minio.PutObjectOptions{ContentType: contentType})
		rc.Close()
		if err != nil {
			updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to upload file to MinIO: %v", err))
			return
		}
	}

	// 1. DB ステータスを 'public' に更新
	entryPointPath := "index.html"
	_, err = db.Exec("UPDATE static_sites SET status = 'public', entry_point_path = $1, processing_details = NULL WHERE id = $2", entryPointPath, siteID)
	if err != nil {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to update static site status to public: %v", err))
		return
	}

	// 2. 本番化成功後に SFSP MinIO (clean-files) からファイルを削除
	if downloadedKey != "" {
		if err := sfspMinioClient.RemoveObject(ctx, sfspBucketName, downloadedKey, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("[SiteID: %s] Failed to delete clean file from SFSP MinIO (%s): %v", siteID, downloadedKey, err)
		} else {
			logger.Infof("[SiteID: %s] Successfully deleted clean file from SFSP MinIO (%s)", siteID, downloadedKey)
		}
	}

	logger.Infof("Successfully processed static site job for SiteID: %s", siteID)
}

func processStaticSiteThumbnail(ctx context.Context, event *event.ScanCompletionEvent) {
	var siteID string
	logger.Debugf("Looking up static site by thumbnail JobID=%s", event.JobID)
	err := db.QueryRow("SELECT id FROM static_sites WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", event.JobID).Scan(&siteID)
	if err != nil {
		logger.Errorf("[Thumbnail JobID: %s] ERROR: Could not find a matching static_site record: %v", event.JobID, err)
		return
	}
	logger.Infof("[SiteID: %s] Found matching static site record for thumbnail SFSP JobID %s", siteID, event.JobID)

	if event.FinalStatus != "clean" {
		logger.Infof("[SiteID: %s] Thumbnail scan result is '%s'. Aborting thumbnail processing.", siteID, event.FinalStatus)
		_, err = db.Exec(ctx, "UPDATE static_sites SET thumbnail_sfsp_job_id = NULL WHERE id = $1", siteID)
		if err != nil {
			logger.Errorf("[SiteID: %s] Failed to clear thumbnail scan job: %v", siteID, err)
		}
		return
	}

	logger.Infof("[SiteID: %s] Thumbnail is clean. Processing thumbnail...", siteID)

	fileIDStr := fmt.Sprintf("%v", event.FileID)
	var keysToTry []string
	if fileIDStr != "" && fileIDStr != "<nil>" {
		keysToTry = append(keysToTry, fileIDStr)
		keysToTry = append(keysToTry, fmt.Sprintf("%s/%s", fileIDStr, event.Filename))
	}
	if event.SHA256 != "" {
		keysToTry = append(keysToTry, fmt.Sprintf("%s/%s", event.SHA256, event.Filename))
		keysToTry = append(keysToTry, event.SHA256)
	}
	if event.Filename != "" {
		keysToTry = append(keysToTry, event.Filename)
	}

	tempThumbPath := filepath.Join(os.TempDir(), fmt.Sprintf("site-thumb-%s-%s", siteID, event.Filename))
	var downloadErr error
	downloadSuccess := false

	for _, sfspObjectName := range keysToTry {
		logger.Infof("[SiteID: %s] Attempting thumbnail download with key: %s", siteID, sfspObjectName)
		downloadErr = sfspMinioClient.FGetObject(ctx, sfspBucketName, sfspObjectName, tempThumbPath, minio.GetObjectOptions{})
		if downloadErr == nil {
			downloadSuccess = true
			logger.Infof("[SiteID: %s] Successfully downloaded thumbnail %s from SFSP", siteID, sfspObjectName)
			break
		}
		logger.Warnf("[SiteID: %s] Failed to download thumbnail with key (%s): %v", siteID, sfspObjectName, downloadErr)
	}

	if !downloadSuccess {
		logger.Errorf("[SiteID: %s] ERROR: Download thumbnail from SFSP failed after retries: %v", siteID, downloadErr)
		return
	}
	defer os.Remove(tempThumbPath)

	f, err := os.Open(tempThumbPath)
	if err != nil {
		logger.Errorf("[SiteID: %s] ERROR: Failed to open downloaded thumbnail: %v", siteID, err)
		return
	}
	defer f.Close()

	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	contentType := http.DetectContentType(buffer[:n])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("[SiteID: %s] ERROR: Failed to reset thumbnail file: %v", siteID, err)
		return
	}

	stat, err := f.Stat()
	if err != nil {
		logger.Errorf("[SiteID: %s] ERROR: Failed to stat thumbnail file: %v", siteID, err)
		return
	}

	thumbnailObjectName := fmt.Sprintf("thumbnails/%s%s", siteID, filepath.Ext(event.Filename))
	_, err = minioClient.PutObject(ctx, minioBucket, thumbnailObjectName, f, stat.Size(), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		logger.Errorf("[SiteID: %s] ERROR: Failed to upload thumbnail to static-site MinIO: %v", siteID, err)
		return
	}

	thumbnailURL := fmt.Sprintf("/static-sites/thumbnails/%s%s", siteID, filepath.Ext(event.Filename))
	_, err = db.Exec(ctx, "UPDATE static_sites SET thumbnail_url = $1, thumbnail_sfsp_job_id = NULL WHERE id = $2", thumbnailURL, siteID)
	if err != nil {
		logger.Errorf("[SiteID: %s] ERROR: Failed to update thumbnail URL in DB: %v", siteID, err)
		return
	}

	logger.Infof("[SiteID: %s] Thumbnail processing complete.", siteID)
}