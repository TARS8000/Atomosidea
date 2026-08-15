package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atmosidea/shared/config" // shared/config をインポート
	"github.com/atmosidea/shared/event"  // shared/event をインポート
	// "github.com/atmosidea/shared/model"  // shared/model をインポート (未使用のため削除)
	"github.com/atmosidea/shared/queue"  // shared/queue をインポート
	// "github.com/go-redis/redis/v8" // redis/v8 を使用 (未使用のため削除)
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap" // zap をインポート
)

var (
	db               *pgxpool.Pool
	gameMinioClient  *minio.Client // For uploading final game assets
	sfspMinioClient  *minio.Client // For downloading the clean file from SFSP
	gameBucketName   string
	sfspBucketName   = "clean-files" // Hardcoded as per SFSP design
	tempDir          = "./temp"
	appURL           *url.URL
	logger           *zap.SugaredLogger // logger を追加
)

func main() {
	// Initialize Zap for logging
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar() // グローバル logger に設定

	ctx := context.Background()

	databaseUrl := os.Getenv("DATABASE_URL")
	appUrlStr := os.Getenv("APP_URL")

	appURL, err = url.Parse(appUrlStr)
	if err != nil {
		logger.Fatalf("Invalid APP_URL: %v", err)
	}

	// Game Storage (for final assets)
	gameMinioEndpoint := os.Getenv("MINIO_ENDPOINT")
	gameMinioAccessKey := os.Getenv("MINIO_ACCESS_KEY_ID")
	gameMinioSecretKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	gameMinioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	gameBucketName = os.Getenv("MINIO_BUCKET_NAME")

	// SFSP Storage (for downloading clean files)
	sfspMinioEndpoint := os.Getenv("SFSP_MINIO_ENDPOINT")
	sfspMinioAccessKey := os.Getenv("SFSP_MINIO_ACCESS_KEY_ID")
	sfspMinioSecretKey := os.Getenv("SFSP_MINIO_SECRET_ACCESS_KEY")
	sfspMinioUseSSL := os.Getenv("SFSP_MINIO_USE_SSL") == "true"

	// Load configuration
	cfg, err := config.LoadConfig() // shared/config.LoadConfig() を使用
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		logger.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	gameMinioClient, err = minio.New(gameMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(gameMinioAccessKey, gameMinioSecretKey, ""),
		Secure: gameMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to Game MinIO: %v", err)
	}

	// Ensure the 'games' bucket exists and has the correct policy
	if err := ensureBucket(ctx, gameMinioClient, gameBucketName); err != nil {
		logger.Fatalf("Failed to ensure 'games' bucket: %v", err)
	}

	sfspMinioClient, err = minio.New(sfspMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspMinioAccessKey, sfspMinioSecretKey, ""),
		Secure: sfspMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to SFSP MinIO: %v", err)
	}

	// Redis クライアントの初期化を shared/queue.ConnectRedis に置き換え
	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, os.ModePerm)

	logger.Info("Game Worker started. Waiting for scan completion events...")

	// completionQueue を専用キュー名に置き換え
	gameCompletionQueue := queue.GameCompletionQueue // shared/queue.GameCompletionQueue を使用

	for {
		// BRPOP のキュー名を専用キュー名に置き換え
		result, err := queue.RedisClient.BRPop(ctx, 0, gameCompletionQueue).Result()
		if err != nil {
			logger.Errorf("Error fetching event from Redis: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if len(result) < 2 {
			continue
		}

		eventPayload := result[1]
		logger.Infof("Received event: %s", eventPayload)

		var event event.ScanCompletionEvent // shared/event.ScanCompletionEvent を使用
		if err := json.Unmarshal([]byte(eventPayload), &event); err != nil {
			logger.Errorf("ERROR: Unmarshalling event payload: %v", err)
			continue
		}

		// Filter events based on TargetService (念のため残すが、専用キューなので不要になるはず)
		if event.TargetService != "game" {
			logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s), not a game event.", event.TargetService, event.JobID)
			continue
		}

		processGame(ctx, &event)
	}
}

func processGame(ctx context.Context, event *event.ScanCompletionEvent) { // shared/event.ScanCompletionEvent を使用
	// Find the game record using the SFSP job ID
	var gameID string
	err := db.QueryRow(ctx, "SELECT id FROM games WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", event.JobID).Scan(&gameID)
	if err != nil {
		logger.Errorf("[SFSP JobID: %s] ERROR: Could not find a matching game record in app-db: %v", event.JobID, err)
		return
	}
	logger.Infof("[GameID: %s] Found matching game record for SFSP JobID %s", gameID, event.JobID)

	// Handle non-clean files
	if event.FinalStatus != "clean" {
		logger.Infof("[GameID: %s] Scan result is '%s'. Aborting processing.", gameID, event.FinalStatus)
		finalStatus := event.FinalStatus
		if finalStatus == "malicious" || finalStatus == "suspicious" {
			finalStatus = "quarantined"
		}
		updateGameStatus(ctx, gameID, finalStatus, fmt.Sprintf("File scan failed with status: %s", event.FinalStatus))
		return
	}

	logger.Infof("[GameID: %s] File is clean. Starting game processing...", gameID)
	updateGameStatus(ctx, gameID, "processing", "スキャン完了。ゲームファイルの処理を開始しました...")

	localZipPath := filepath.Join(tempDir, event.Filename)
	unzipPath := filepath.Join(tempDir, fmt.Sprintf("game-%s", gameID))
	defer os.Remove(localZipPath)
	defer os.RemoveAll(unzipPath)

	// Download the clean file from SFSP's clean-files bucket
	sfspObjectName := fmt.Sprintf("%s/%s", event.SHA256, event.Filename)
	logger.Infof("[GameID: %s] Downloading %s from SFSP MinIO bucket '%s'...", gameID, sfspObjectName, sfspBucketName)
	updateGameStatus(ctx, gameID, "processing", "クリーンなゲームファイルをダウンロード中...")
	if err := sfspMinioClient.FGetObject(ctx, sfspBucketName, sfspObjectName, localZipPath, minio.GetObjectOptions{}); err != nil {
		updateGameStatus(ctx, gameID, "error", fmt.Sprintf("Failed to download clean file from SFSP MinIO: %v", err))
		logger.Errorf("[GameID: %s] ERROR: Download from SFSP failed: %v", gameID, err)
		return
	}
	logger.Infof("[GameID: %s] Downloaded clean file to %s", gameID, localZipPath)

	// --- From here, the original logic of the worker continues ---

	logger.Infof("[GameID: %s] Unzipping %s...", gameID, localZipPath)
	updateGameStatus(ctx, gameID, "processing", "ゲームファイルを解凍中...")
	if err := unzip(localZipPath, unzipPath); err != nil {
		updateGameStatus(ctx, gameID, "error", fmt.Sprintf("Failed to unzip file: %v", err))
		logger.Errorf("[GameID: %s] ERROR: Unzip failed: %v", gameID, err)
		return
	}

	logger.Infof("[GameID: %s] Finding game root in %s...", gameID, unzipPath)
	gameRoot, err := findGameRoot(unzipPath)
	if err != nil {
		updateGameStatus(ctx, gameID, "error", err.Error())
		logger.Errorf("[GameID: %s] ERROR: Game root not found: %v", gameID, err)
		return
	}

	logger.Infof("[GameID: %s] Extracting native resolution...", gameID)
	nativeWidth, nativeHeight, err := extractNativeResolution(filepath.Join(gameRoot, "index.html"))
	if err != nil {
		logger.Warnf("[GameID: %s] WARNING: Could not extract native resolution: %v. Using defaults.", gameID, err)
		nativeWidth = 960
		nativeHeight = 600
	}

	logger.Infof("[GameID: %s] Modifying index.html...", gameID)
	if err := modifyIndexHtml(filepath.Join(gameRoot, "index.html")); err != nil {
		updateGameStatus(ctx, gameID, "error", fmt.Sprintf("Failed to modify index.html: %v", err))
		logger.Errorf("[GameID: %s] ERROR: index.html modification failed: %v", gameID, err)
		return
	}

	// publicPathPrefix の定義を、使用される最初の場所よりも前に移動
	publicPathPrefix := fmt.Sprintf("%s/", gameID) // ここに移動

	hostParts := strings.Split(appURL.Host, ":")
	domain := hostParts[0]
	gameURL := fmt.Sprintf("%s://%s.%s:%s/index.html", appURL.Scheme, gameID, domain, appURL.Port())
	logger.Infof("[GameID: %s] Uploading game files to Game MinIO with prefix %s...", gameID, publicPathPrefix)
	updateGameStatus(ctx, gameID, "processing", "ゲームファイルをストレージにアップロード中...")
	if err := uploadDirectory(ctx, gameRoot, publicPathPrefix); err != nil {
		updateGameStatus(ctx, gameID, "error", fmt.Sprintf("Failed to upload unzipped files: %v", err))
		logger.Errorf("[GameID: %s] ERROR: Upload to Game MinIO failed: %v", gameID, err)
		return
	}

	logger.Infof("[GameID: %s] Updating game status in DB to 'public'...", gameID)
	if err := updateGameStatusWithResolution(ctx, gameID, "public", gameURL, nativeWidth, nativeHeight); err != nil {
		logger.Errorf("CRITICAL: [GameID: %s] Failed to update DB status to public: %v", gameID, err)
		return
	}

	logger.Infof("[GameID: %s] Processing complete. Game is now public.", gameID)
}

func findGameRoot(basePath string) (string, error) {
	if _, err := os.Stat(filepath.Join(basePath, "index.html")); err == nil {
		return basePath, nil
	}
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		potentialRoot := filepath.Join(basePath, entries[0].Name())
		if _, err := os.Stat(filepath.Join(potentialRoot, "index.html")); err == nil {
			return potentialRoot, nil
		}
	}
	return "", fmt.Errorf("could not find a valid game structure; index.html not found")
}

func extractNativeResolution(indexPath string) (width, height int, err error) {
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return 0, 0, err
	}
	stringContent := string(content)
	reWidth := regexp.MustCompile(`canvas\.style\.width\s*=\s*"(\d+)px"`)
	reHeight := regexp.MustCompile(`canvas\.style\.height\s*=\s*"(\d+)px"`)
	widthMatch := reWidth.FindStringSubmatch(stringContent)
	heightMatch := reHeight.FindStringSubmatch(stringContent)
	if len(widthMatch) > 1 && len(heightMatch) > 1 {
		w, _ := strconv.Atoi(widthMatch[1])
		h, _ := strconv.Atoi(heightMatch[1])
		return w, h, nil
	}
	reCanvasTag := regexp.MustCompile(`<canvas[^>]*width="(\d+)"[^>]*height="(\d+)"`)
	canvasTagMatch := reCanvasTag.FindStringSubmatch(stringContent)
	if len(canvasTagMatch) > 2 {
		w, _ := strconv.Atoi(canvasTagMatch[1])
		h, _ := strconv.Atoi(canvasTagMatch[2])
		return w, h, nil
	}
	return 0, 0, fmt.Errorf("could not find resolution")
}

func modifyIndexHtml(indexPath string) error {
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	overrideCss := `<style>html, body { margin: 0; padding: 0; overflow: hidden; } #unity-footer { display: none !important; }</style>`
	modifiedContent := strings.Replace(string(content), "</head>", overrideCss+"</head>", 1)
	return os.WriteFile(indexPath, []byte(modifiedContent), 0644)
}

func updateGameStatus(ctx context.Context, gameID, status, details string) error {
	_, err := db.Exec(ctx, "UPDATE games SET status = $1, processing_details = $2, updated_at = NOW() WHERE id = $3", status, details, gameID)
	return err
}

func updateGameStatusWithResolution(ctx context.Context, gameID, status, gameURL string, nativeWidth, nativeHeight int) error {
	_, err := db.Exec(ctx, "UPDATE games SET status = $1, game_url = $2, native_width = $3, native_height = $4, processing_details = NULL WHERE id = $5",
		status, gameURL, nativeWidth, nativeHeight, gameID)
	return err
}

func uploadDirectory(ctx context.Context, localPath, remotePathPrefix string) error {
	return filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, _ := filepath.Rel(localPath, path)
			remotePath := filepath.Join(remotePathPrefix, relPath)
			remotePath = strings.ReplaceAll(remotePath, "\\", "/")

			contentType := "application/octet-stream"
			contentEncoding := ""

			switch {
			case strings.HasSuffix(path, ".html"):
				contentType = "text/html"
			case strings.HasSuffix(path, ".js.br"):
				contentType = "application/javascript"
				contentEncoding = "br"
			case strings.HasSuffix(path, ".js"):
				contentType = "application/javascript"
			case strings.HasSuffix(path, ".css"):
				contentType = "text/css"
			case strings.HasSuffix(path, ".wasm.br"):
				contentType = "application/wasm"
				contentEncoding = "br"
			case strings.HasSuffix(path, ".wasm"):
				contentType = "application/wasm"
			case strings.HasSuffix(path, ".json"):
				contentType = "application/json"
			case strings.HasSuffix(path, ".data.br"):
				contentType = "application/octet-stream"
				contentEncoding = "br"
			}

			_, err = gameMinioClient.FPutObject(ctx, gameBucketName, remotePath, path, minio.PutObjectOptions{ContentType: contentType, ContentEncoding: contentEncoding})
			return err
		}
		return nil
	})
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func ensureBucket(ctx context.Context, client *minio.Client, bucketName string) error {
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("failed to check existence of bucket %s: %w", bucketName, err)
	}
	if !exists {
		logger.Infof("Bucket %s does not exist, creating...", bucketName)
		if err := client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("failed to create bucket %s: %w", bucketName, err)
		}
		logger.Infof("Bucket %s created successfully.", bucketName)
	}

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + bucketName + `/*"]}]}`
	if err := client.SetBucketPolicy(ctx, bucketName, policy); err != nil {
		return fmt.Errorf("failed to set public policy for bucket %s: %w", bucketName, err)
	}
	logger.Infof("Public read policy set for bucket %s.", bucketName)

	return nil
}