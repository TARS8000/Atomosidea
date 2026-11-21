package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db              *pgxpool.Pool
	gameMinioClient *minio.Client // For uploading final game assets
	sfspMinioClient *minio.Client // For downloading the clean file from SFSP
	gameBucketName  string
	sfspBucketName  = "clean-files" // Hardcoded as per SFSP design
	tempDir         = "./temp"
	appURL          *url.URL
	logger          *zap.SugaredLogger

	// zip 展開の上限（zip bomb 対策。環境変数で上書き可能）
	maxExtractedBytes int64 = 2 << 30  // GAME_MAX_EXTRACTED_BYTES: 展開後の合計
	maxZipEntries     int64 = 20000    // GAME_MAX_ZIP_ENTRIES: エントリー数
	maxEntryBytes     int64 = 1 << 30  // GAME_MAX_ENTRY_BYTES: 1 エントリーの展開後サイズ
	maxThumbnailBytes int64 = 10 << 20 // MAX_IMAGE_UPLOAD_BYTES: サムネイル
)

// index.html はメモリに読み込んで書き換えるので、別途上限を設ける
const maxIndexHTMLBytes int64 = 32 << 20

// 完了イベントが games の INSERT / ジョブ ID の UPDATE より先に届いた場合に備えた再試行間隔
var rowLookupBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// 処理を終えたゲームの status。これらの行に対するイベントの再配信は無視する。
var terminalGameStatuses = map[string]bool{
	"public":      true,
	"quarantined": true,
	"failed":      true,
	"error":       true,
	"invalid":     true,
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
		logger.Warnf("Invalid %s=%q; using default %d", name, v, def)
		return def
	}
	return n
}

func main() {
	// Initialize Zap for logging
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	ctx := context.Background()

	databaseUrl := os.Getenv("DATABASE_URL")
	appUrlStr := os.Getenv("APP_URL")

	appURL, err = url.Parse(appUrlStr)
	if err != nil {
		logger.Fatalf("Invalid APP_URL: %v", err)
	}

	maxExtractedBytes = envInt64("GAME_MAX_EXTRACTED_BYTES", maxExtractedBytes)
	maxZipEntries = envInt64("GAME_MAX_ZIP_ENTRIES", maxZipEntries)
	maxEntryBytes = envInt64("GAME_MAX_ENTRY_BYTES", maxEntryBytes)
	maxThumbnailBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", maxThumbnailBytes)

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
	cfg, err := config.LoadConfig()
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

	// 'games' バケットの作成と匿名ダウンロードの設定は minio-init が行う

	sfspMinioClient, err = minio.New(sfspMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sfspMinioAccessKey, sfspMinioSecretKey, ""),
		Secure: sfspMinioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to SFSP MinIO: %v", err)
	}

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, 0o755)

	gameCompletionQueue := queue.GameCompletionQueue
	thumbnailCompletionQueue := queue.GameThumbnailCompletionQueue

	// 前回の起動中に取り出したまま Ack していないイベントを元のキューに戻す
	for _, q := range []string{gameCompletionQueue, thumbnailCompletionQueue} {
		n, err := queue.RecoverProcessing(ctx, q)
		if err != nil {
			logger.Fatalf("Failed to recover unacknowledged events for %s: %v", q, err)
		}
		if n > 0 {
			logger.Infof("Recovered %d unacknowledged event(s) back to %s", n, q)
		}
	}

	logger.Infof("Game Worker started (max extracted %d bytes, max entries %d, max entry %d bytes). Waiting for scan completion events...",
		maxExtractedBytes, maxZipEntries, maxEntryBytes)

	// BRPOPLPUSH は 1 つのキューしか待てないので、キューごとに consumer を動かす
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		consumeQueue(ctx, gameCompletionQueue, "game", processGame)
	}()
	go func() {
		defer wg.Done()
		consumeQueue(ctx, thumbnailCompletionQueue, "game-thumbnail", processGameThumbnail)
	}()
	wg.Wait()
}

// consumeQueue はイベントを DequeueReliable で取り出し、終端の状態を DB に記録できたら Ack する。
// handler がエラーを返した（DB に終端の状態を書けなかった）イベントは処理中リストに残り、
// 次回起動時の RecoverProcessing で再処理される。
func consumeQueue(ctx context.Context, queueName, targetService string, handler func(context.Context, *event.ScanCompletionEvent) error) {
	for {
		payload, err := queue.DequeueReliable(ctx, queueName, 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Errorf("Error fetching event from Redis queue %s: %v. Retrying in 5 seconds...", queueName, err)
			time.Sleep(5 * time.Second)
			continue
		}
		logger.Infof("Received event from queue [%s]: %s", queueName, payload)

		var ev event.ScanCompletionEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			logger.Errorf("ERROR: Unmarshalling event payload from %s: %v (discarding)", queueName, err)
			ack(ctx, queueName, payload)
			continue
		}
		if ev.TargetService != targetService {
			logger.Infof("INFO: Skipping event for TargetService '%s' (Job ID: %s) on queue %s.", ev.TargetService, ev.JobID, queueName)
			ack(ctx, queueName, payload)
			continue
		}

		if err := handler(ctx, &ev); err != nil {
			logger.Errorf("[SFSP JobID: %s] Could not record a terminal state; leaving event unacknowledged for recovery on restart: %v", ev.JobID, err)
			continue
		}
		ack(ctx, queueName, payload)
	}
}

func ack(ctx context.Context, queueName, payload string) {
	if err := queue.Ack(ctx, queueName, payload); err != nil {
		logger.Errorf("Failed to ack event on %s: %v", queueName, err)
	}
}

// findRowByJobID は job_id に一致する行を探す。完了イベントが行の作成やジョブ ID の保存より
// 先に届くことがあるので、見つからなければ間隔を空けて数回探し直す。
// 最後まで見つからなければ found=false（エラーなし）を返す。DB エラーが続いた場合は err を返す。
func findRowByJobID(ctx context.Context, query string, jobID interface{}, dest ...interface{}) (bool, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		err := db.QueryRow(ctx, query, jobID).Scan(dest...)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			lastErr = nil
		} else {
			lastErr = err
			logger.Warnf("[SFSP JobID: %v] DB error while looking up row: %v", jobID, err)
		}
		if attempt >= len(rowLookupBackoff) {
			break
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(rowLookupBackoff[attempt]):
		}
	}
	return false, lastErr
}

func processGame(ctx context.Context, event *event.ScanCompletionEvent) error {
	// Find the game record using the SFSP job ID
	var gameID, currentStatus string
	found, err := findRowByJobID(ctx,
		"SELECT id, COALESCE(status, '') FROM games WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1",
		event.JobID, &gameID, &currentStatus)
	if err != nil {
		return fmt.Errorf("lookup game by job id: %w", err)
	}
	if !found {
		logger.Errorf("[SFSP JobID: %s] ERROR: No matching game record in app-db after retries; discarding event", event.JobID)
		return nil
	}
	logger.Infof("[GameID: %s] Found matching game record for SFSP JobID %s (status %s)", gameID, event.JobID, currentStatus)

	if terminalGameStatuses[currentStatus] {
		logger.Infof("[GameID: %s] Game is already in terminal status '%s'; ignoring redelivered event.", gameID, currentStatus)
		return nil
	}

	// Handle non-clean files
	if event.FinalStatus != "clean" {
		logger.Infof("[GameID: %s] Scan result is '%s'. Aborting processing.", gameID, event.FinalStatus)
		finalStatus := event.FinalStatus
		if finalStatus == "malicious" || finalStatus == "suspicious" {
			finalStatus = "quarantined"
		} else if finalStatus != "invalid" {
			finalStatus = "failed"
		}
		return updateGameStatus(ctx, gameID, finalStatus, fmt.Sprintf("File scan failed with status: %s", event.FinalStatus))
	}

	// 処理中のエラーはすべて status = 'failed' で記録する
	fail := func(msg string, err error) error {
		logger.Errorf("[GameID: %s] ERROR: %s: %v", gameID, msg, err)
		return updateGameStatus(ctx, gameID, "failed", fmt.Sprintf("%s: %v", msg, err))
	}

	logger.Infof("[GameID: %s] File is clean. Starting game processing...", gameID)
	updateGameStatus(ctx, gameID, "processing", "スキャン完了。ゲームファイルの処理を開始しました...")

	// 一時ファイル名にユーザーのファイル名は使わない（パストラバーサル・同名衝突の防止）
	localZipPath := filepath.Join(tempDir, fmt.Sprintf("game-%s.zip", gameID))
	unzipPath := filepath.Join(tempDir, fmt.Sprintf("game-%s", gameID))
	os.RemoveAll(unzipPath)
	defer os.Remove(localZipPath)
	defer os.RemoveAll(unzipPath)

	// sfsp はクリーンなファイルを clean-files の <file_id> に保存する
	objectKey := event.FileID.String()

	updateGameStatus(ctx, gameID, "processing", "クリーンなゲームファイルをダウンロード中...")

	logger.Infof("[GameID: %s] Downloading key '%s' from SFSP MinIO bucket '%s'...", gameID, objectKey, sfspBucketName)
	if err := sfspMinioClient.FGetObject(ctx, sfspBucketName, objectKey, localZipPath, minio.GetObjectOptions{}); err != nil {
		return fail("Failed to download clean file from SFSP MinIO", err)
	}

	logger.Infof("[GameID: %s] Unzipping %s...", gameID, localZipPath)
	updateGameStatus(ctx, gameID, "processing", "ゲームファイルを解凍中...")
	if err := unzip(localZipPath, unzipPath); err != nil {
		return fail("Failed to unzip file", err)
	}
	os.Remove(localZipPath)

	logger.Infof("[GameID: %s] Finding game root in %s...", gameID, unzipPath)
	gameRoot, err := findGameRoot(unzipPath)
	if err != nil {
		return fail("Invalid game structure", err)
	}
	indexPath := filepath.Join(gameRoot, "index.html")
	if st, err := os.Stat(indexPath); err != nil || !st.Mode().IsRegular() || st.Size() > maxIndexHTMLBytes {
		return fail("Invalid index.html", fmt.Errorf("index.html must be a regular file of at most %d bytes", maxIndexHTMLBytes))
	}

	logger.Infof("[GameID: %s] Extracting native resolution...", gameID)
	nativeWidth, nativeHeight, err := extractNativeResolution(indexPath)
	if err != nil {
		logger.Warnf("[GameID: %s] WARNING: Could not extract native resolution: %v. Using defaults.", gameID, err)
		// init.sql / ensureGameSchema の既定値と揃える
		nativeWidth = 1280
		nativeHeight = 720
	}

	logger.Infof("[GameID: %s] Modifying index.html...", gameID)
	if err := modifyIndexHtml(indexPath); err != nil {
		return fail("Failed to modify index.html", err)
	}

	publicPathPrefix := fmt.Sprintf("%s/", gameID)

	hostParts := strings.Split(appURL.Host, ":")
	domain := hostParts[0]
	gameURL := fmt.Sprintf("%s://%s.%s:%s/index.html", appURL.Scheme, gameID, domain, appURL.Port())
	logger.Infof("[GameID: %s] Uploading game files to Game MinIO with prefix %s...", gameID, publicPathPrefix)
	updateGameStatus(ctx, gameID, "processing", "ゲームファイルをストレージにアップロード中...")
	if err := uploadDirectory(ctx, gameRoot, publicPathPrefix); err != nil {
		return fail("Failed to upload unzipped files", err)
	}

	logger.Infof("[GameID: %s] Updating game status in DB to 'public'...", gameID)
	if err := updateGameStatusWithResolution(ctx, gameID, "public", gameURL, nativeWidth, nativeHeight); err != nil {
		return fmt.Errorf("CRITICAL: failed to update DB status to public for game %s: %w", gameID, err)
	}

	logger.Infof("[GameID: %s] Processing complete. Game is now public.", gameID)
	return nil
}

func processGameThumbnail(ctx context.Context, event *event.ScanCompletionEvent) error {
	var gameID string
	var currentThumbnailURL string
	found, err := findRowByJobID(ctx,
		"SELECT id, COALESCE(thumbnail_url, '') FROM games WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1",
		event.JobID, &gameID, &currentThumbnailURL)
	if err != nil {
		return fmt.Errorf("lookup game by thumbnail job id: %w", err)
	}
	if !found {
		logger.Errorf("[Thumbnail JobID: %s] ERROR: No matching game record after retries; discarding event", event.JobID)
		return nil
	}
	logger.Infof("[GameID: %s] Found matching game record for thumbnail SFSP JobID %s", gameID, event.JobID)

	// サムネイル処理の終端の状態は「thumbnail_sfsp_job_id を NULL に戻す」こと（フロントエンドはこれを待つ）。
	// 同じ行に別の新しいジョブが入っていたら上書きしない。
	clearJob := func() error {
		_, err := db.Exec(ctx, "UPDATE games SET thumbnail_sfsp_job_id = NULL WHERE id = $1 AND thumbnail_sfsp_job_id = $2", gameID, event.JobID)
		return err
	}
	fail := func(msg string, err error) error {
		logger.Errorf("[GameID: %s] ERROR: %s: %v", gameID, msg, err)
		return clearJob()
	}

	if event.FinalStatus != "clean" {
		logger.Infof("[GameID: %s] Thumbnail scan result is '%s'. Aborting thumbnail processing.", gameID, event.FinalStatus)
		return clearJob()
	}

	logger.Infof("[GameID: %s] Thumbnail is clean. Processing thumbnail...", gameID)

	// sfsp はクリーンなファイルを clean-files の <file_id> に保存する
	objectKey := event.FileID.String()
	logger.Infof("[GameID: %s] Downloading thumbnail key '%s' from SFSP MinIO bucket '%s'...", gameID, objectKey, sfspBucketName)
	data, err := downloadLimited(ctx, sfspBucketName, objectKey, maxThumbnailBytes)
	if err != nil {
		return fail("Download thumbnail from SFSP failed", err)
	}

	contentType := http.DetectContentType(data)
	if _, ok := imageExtensions[contentType]; !ok {
		return fail("Rejected thumbnail", fmt.Errorf("non-image content type %q", contentType))
	}

	// Name the thumbnail object after the game ID so the thumbnail is directly
	// identifiable with its game (e.g. thumbnails/<game_id> mirrors games/<game_id>/).
	thumbnailObjectName := fmt.Sprintf("thumbnails/%s", gameID)

	// Reclaim storage: delete the previous thumbnail before uploading the new one.
	if currentThumbnailURL != "" {
		oldObjectName := strings.TrimPrefix(currentThumbnailURL, "/games/")
		if oldObjectName != thumbnailObjectName {
			if err := gameMinioClient.RemoveObject(ctx, gameBucketName, oldObjectName, minio.RemoveObjectOptions{}); err != nil {
				logger.Warnf("[GameID: %s] WARNING: Failed to delete previous thumbnail %s: %v", gameID, oldObjectName, err)
			} else {
				logger.Infof("[GameID: %s] Deleted previous thumbnail %s", gameID, oldObjectName)
			}
		}
	}

	_, err = gameMinioClient.PutObject(ctx, gameBucketName, thumbnailObjectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fail("Failed to upload thumbnail to game MinIO", err)
	}

	thumbnailURL := fmt.Sprintf("/games/%s", thumbnailObjectName)
	_, err = db.Exec(ctx, "UPDATE games SET thumbnail_url = $1, thumbnail_sfsp_job_id = NULL WHERE id = $2 AND thumbnail_sfsp_job_id = $3", thumbnailURL, gameID, event.JobID)
	if err != nil {
		return fmt.Errorf("failed to update thumbnail URL in DB for game %s: %w", gameID, err)
	}

	logger.Infof("[GameID: %s] Thumbnail processing complete.", gameID)
	return nil
}

// downloadLimited はオブジェクトを最大 limit バイトまでメモリに読み込む。超えたらエラー。
func downloadLimited(ctx context.Context, bucket, key string, limit int64) ([]byte, error) {
	obj, err := sfspMinioClient.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	st, err := obj.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size > limit {
		return nil, fmt.Errorf("object size %d exceeds limit %d", st.Size, limit)
	}
	data, err := io.ReadAll(io.LimitReader(obj, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("object exceeds limit %d", limit)
	}
	return data, nil
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
	if err != nil {
		logger.Errorf("[GameID: %s] Failed to update status to '%s': %v", gameID, status, err)
	}
	return err
}

func updateGameStatusWithResolution(ctx context.Context, gameID, status, gameURL string, nativeWidth, nativeHeight int) error {
	_, err := db.Exec(ctx, "UPDATE games SET status = $1, game_url = $2, native_width = $3, native_height = $4, processing_details = NULL, updated_at = NOW() WHERE id = $5",
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
			case strings.HasSuffix(path, ".js.gz"):
				contentType = "application/javascript"
				contentEncoding = "gzip"
			case strings.HasSuffix(path, ".js"):
				contentType = "application/javascript"
			case strings.HasSuffix(path, ".css"):
				contentType = "text/css"
			case strings.HasSuffix(path, ".wasm.br"):
				contentType = "application/wasm"
				contentEncoding = "br"
			case strings.HasSuffix(path, ".wasm.gz"):
				contentType = "application/wasm"
				contentEncoding = "gzip"
			case strings.HasSuffix(path, ".wasm"):
				contentType = "application/wasm"
			case strings.HasSuffix(path, ".json"):
				contentType = "application/json"
			case strings.HasSuffix(path, ".data.br"):
				contentType = "application/octet-stream"
				contentEncoding = "br"
			case strings.HasSuffix(path, ".data.gz"):
				contentType = "application/octet-stream"
				contentEncoding = "gzip"
			}

			_, err = gameMinioClient.FPutObject(ctx, gameBucketName, remotePath, path, minio.PutObjectOptions{ContentType: contentType, ContentEncoding: contentEncoding})
			return err
		}
		return nil
	})
}

// unzip は src を dest に展開する。zip bomb / zip slip 対策として次を強制する:
//   - エントリー数 <= maxZipEntries
//   - 1 エントリーの展開後サイズ <= maxEntryBytes、合計 <= maxExtractedBytes
//     （ヘッダーの申告値で先に弾き、実際の展開も io.CopyN で上限までしか読まない）
//   - 絶対パス・".." を含むパス・dest の外に出るパスは拒否
//   - シンボリックリンクや特殊ファイルは展開しない
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	if int64(len(r.File)) > maxZipEntries {
		return fmt.Errorf("zip has too many entries (%d > %d)", len(r.File), maxZipEntries)
	}
	var declared uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > uint64(maxEntryBytes) {
			return fmt.Errorf("zip entry %q is too large (%d > %d bytes)", f.Name, f.UncompressedSize64, maxEntryBytes)
		}
		declared += f.UncompressedSize64
		if declared > uint64(maxExtractedBytes) {
			return fmt.Errorf("zip expands to more than %d bytes", maxExtractedBytes)
		}
	}

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}

	var total int64
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || (len(name) >= 2 && name[1] == ':') {
			return fmt.Errorf("illegal file path in zip: %q", f.Name)
		}
		cleanName := path.Clean(name)
		if cleanName == "." {
			continue
		}
		if cleanName == ".." || strings.HasPrefix(cleanName, "../") {
			return fmt.Errorf("illegal file path in zip: %q", f.Name)
		}
		fpath := filepath.Join(destAbs, filepath.FromSlash(cleanName))
		rel, err := filepath.Rel(destAbs, fpath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in zip: %q", f.Name)
		}

		mode := f.Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(fpath, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			logger.Warnf("Skipping non-regular zip entry %q (mode %s)", f.Name, mode)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return err
		}

		limit := maxEntryBytes
		if remaining := maxExtractedBytes - total; remaining < limit {
			limit = remaining
		}
		n, err := extractEntry(f, fpath, limit)
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

// extractEntry は 1 エントリーを最大 limit バイトまで書き出し、書いたバイト数を返す。
func extractEntry(f *zip.File, fpath string, limit int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// 既存のファイルやリンクを辿らないよう、作り直してから書く
	os.Remove(fpath)
	outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.CopyN(outFile, rc, limit+1)
	closeErr := outFile.Close()
	if err != nil && err != io.EOF {
		return n, fmt.Errorf("failed to extract %q: %w", f.Name, err)
	}
	if n > limit {
		return n, fmt.Errorf("zip entry %q exceeds the extraction limit (%d bytes)", f.Name, limit)
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}
