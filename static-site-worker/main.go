package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/shared/config" // shared/config をインポート
	"github.com/atmosidea/shared/event"  // shared/event をインポート
	// "github.com/atmosidea/shared/model"  // shared/model をインポート (未使用のため削除)
	"github.com/atmosidea/shared/queue"  // shared/queue をインポート
	// "github.com/go-redis/redis/v8" // redis/v8 を使用 (未使用のため削除)
	"github.com/joho/godotenv"
	_ "github.com/lib/pq" // PostgreSQL driver
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap" // zap をインポート
)

var (
	db              *sql.DB
	minioClient     *minio.Client
	minioBucket     string
	sfspMinioClient *minio.Client
	sfspBucketName  = "clean-files"
	logger          *zap.SugaredLogger // logger を追加
)

func main() {
	// Initialize Zap for logging
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar() // グローバル logger に設定

	err = godotenv.Load()
	if err != nil {
		logger.Warnf("Error loading .env file, assuming production environment: %v", err)
	}

	// Load configuration
	cfg, err := config.LoadConfig() // shared/config.LoadConfig() を使用
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

	// Redis クライアントの初期化を shared/queue.ConnectRedis に置き換え
	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	logger.Info("Static Site Worker started. Waiting for jobs...")

	// completionQueue を専用キュー名に置き換え
	staticSiteCompletionQueue := queue.StaticSiteCompletionQueue // shared/queue.StaticSiteCompletionQueue を使用

	for {
		// BRPOP のキュー名を専用キュー名に置き換え
		result, err := queue.RedisClient.BRPop(context.Background(), 0, staticSiteCompletionQueue).Result()
		if err != nil {
			logger.Errorf("Error popping job from Redis: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		logger.Debugf("Received Redis message: %+v", result) // Debug log

		var event event.ScanCompletionEvent // shared/event.ScanCompletionEvent を使用
		if err := json.Unmarshal([]byte(result[1]), &event); err != nil {
			logger.Errorf("Error unmarshalling event payload: %v", err)
			continue
		}
		logger.Debugf("Parsed event: %+v", event) // Debug log

		// Filter events based on TargetService (念のため残すが、専用キューなので不要になるはず)
		if event.TargetService != "static-site" {
			logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a static site event.", event.TargetService, event.JobID)
			continue
		}

		var siteID string
		logger.Debugf("Looking up site by JobID=%s", event.JobID) // Debug log
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
	) // Debug log

	ctx := context.Background()
	tempZipPath := filepath.Join(os.TempDir(), event.Filename)
	unzipPath := filepath.Join(os.TempDir(), fmt.Sprintf("site-%s", siteID))
	defer os.Remove(tempZipPath)
	defer os.RemoveAll(unzipPath)

	updateProcessingStatus(siteID, "processing", "クリーンなファイルをダウンロード中...")
	sfspObjectName := fmt.Sprintf("%s/%s", event.SHA256, event.Filename)
	if err := sfspMinioClient.FGetObject(ctx, sfspBucketName, sfspObjectName, tempZipPath, minio.GetObjectOptions{}); err != nil {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to download clean file from SFSP MinIO: %v", err))
		return
	}

	updateProcessingStatus(siteID, "processing", "ZIPファイルを解凍中...")
	zipReader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to open zip file: %v", err))
		return
	}
	defer zipReader.Close()

	// 1. index.html のパスを探し出し、親ディレクトリ（ルートフォルダ）を特定する
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

	// 例: "web_cam/index.html" -> "web_cam/"
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

		// 最上位フォルダ名を取り除いて相対パス化する
		relPath := normalizedPath
		if zipRootDir != "" && strings.HasPrefix(normalizedPath, zipRootDir) {
			relPath = strings.TrimPrefix(normalizedPath, zipRootDir)
		}

		rc, err := file.Open()
		if err != nil {
			updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to open file in zip: %v", err))
			return
		}

		// S3/MinIO 用のオブジェクトキー作成（OS依存のバックスラッシュ防止のため path.Join を使用）
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
		rc.Close() // ループ内でのリソースリーク防止のため明示的にクローズ
		if err != nil {
			updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to upload file to MinIO: %v", err))
			return
		}
	}

	// DB上の entry_point_path は常に直下の "index.html" に統一
	entryPointPath := "index.html"
	_, err = db.Exec("UPDATE static_sites SET status = 'public', entry_point_path = $1, processing_details = NULL WHERE id = $2", entryPointPath, siteID)
	if err != nil {
		updateProcessingStatus(siteID, "error", fmt.Sprintf("Failed to update static site status to public: %v", err))
		return
	}

	logger.Infof("Successfully processed static site job for SiteID: %s", siteID)
}