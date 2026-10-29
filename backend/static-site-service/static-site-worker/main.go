package main

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/go-redis/redis/v8"
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

	// 展開・ダウンロードの上限（環境変数で上書き可能）
	maxSiteUploadBytes    int64 = 524288000 // MAX_SITE_UPLOAD_BYTES: clean-files から取得する zip の上限
	maxImageUploadBytes   int64 = 10485760  // MAX_IMAGE_UPLOAD_BYTES: サムネイルの上限
	siteMaxExtractedBytes int64 = 1 << 30   // SITE_MAX_EXTRACTED_BYTES: 展開後の合計サイズの上限
	siteMaxZipEntries     int64 = 20000     // SITE_MAX_ZIP_ENTRIES: zip のエントリ数の上限
	siteMaxEntryBytes     int64 = 268435456 // SITE_MAX_ENTRY_BYTES: 1 エントリあたりの上限
)

// 処理を同時に走らせる最大数（静的サイト・サムネイル合計）
const workerConcurrency = 4

// errLimitExceeded は展開・ダウンロードのサイズ上限を超えたことを表す。
var errLimitExceeded = errors.New("size limit exceeded")

// generateRandomThumbnailName はサムネイルのオブジェクト名に使うランダムな英数字文字列を返す。
// 元のファイル名やサイトIDを名前に使わず、推測不可能な文字列にする。
func generateRandomThumbnailName() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		logger.Errorf("Failed to generate random thumbnail name: %v", err)
		return ""
	}
	return hex.EncodeToString(bytes)
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
		logger.Warnf("Invalid value for %s (%q); using default %d", name, v, def)
		return def
	}
	return n
}

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	maxSiteUploadBytes = envInt64("MAX_SITE_UPLOAD_BYTES", maxSiteUploadBytes)
	maxImageUploadBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", maxImageUploadBytes)
	siteMaxExtractedBytes = envInt64("SITE_MAX_EXTRACTED_BYTES", siteMaxExtractedBytes)
	siteMaxZipEntries = envInt64("SITE_MAX_ZIP_ENTRIES", siteMaxZipEntries)
	siteMaxEntryBytes = envInt64("SITE_MAX_ENTRY_BYTES", siteMaxEntryBytes)

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

	ctx := context.Background()
	queues := []struct {
		name    string
		handler func(payload string) bool
	}{
		{queue.StaticSiteCompletionQueue, handleStaticSiteMessage},
		{queue.SiteThumbnailCompletionQueue, handleThumbnailMessage},
	}

	// 前回の起動中に Ack されずに残ったメッセージをキューへ戻す（コンシューマ起動前に 1 回だけ）
	for _, q := range queues {
		n, err := queue.RecoverProcessing(ctx, q.name)
		if err != nil {
			logger.Fatalf("Failed to recover processing list for %s: %v", q.name, err)
		}
		if n > 0 {
			logger.Infof("Recovered %d unacknowledged message(s) back to %s", n, q.name)
		}
	}

	logger.Info("Static Site Worker started. Waiting for jobs...")

	sem := make(chan struct{}, workerConcurrency)
	for _, q := range queues {
		go consume(ctx, q.name, q.handler, sem)
	}
	select {}
}

// consume は queueName から信頼性のある取り出し（BRPOPLPUSH）を行い、handler を実行する。
// handler が true を返したときだけ Ack する（false = 終端状態を DB に書けなかったので、
// 次回起動時の RecoverProcessing で再処理させる）。
func consume(ctx context.Context, queueName string, handler func(string) bool, sem chan struct{}) {
	for {
		payload, err := queue.DequeueReliable(ctx, queueName, 5*time.Second)
		if err == redis.Nil {
			continue
		}
		if err != nil {
			logger.Errorf("Error popping job from Redis (%s): %v", queueName, err)
			time.Sleep(5 * time.Second)
			continue
		}

		sem <- struct{}{}
		go func(payload string) {
			defer func() { <-sem }()
			ack := true
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Errorf("Panic while handling message from %s: %v", queueName, r)
						ack = true
					}
				}()
				ack = handler(payload)
			}()
			if !ack {
				logger.Warnf("Message from %s left unacknowledged; it will be retried on restart", queueName)
				return
			}
			if err := queue.Ack(ctx, queueName, payload); err != nil {
				logger.Errorf("Failed to ack message on %s: %v", queueName, err)
			}
		}(payload)
	}
}

// lookupWithRetry は job_id に対応する行が DB に現れるまで指数バックオフで待つ。
// アップロード API は sfsp-api の応答を受けてから job_id を書き込むため、
// スキャンが非常に速いと完了イベントが先に届くことがある。
func lookupWithRetry(query string, jobID string, dest ...interface{}) error {
	delay := 500 * time.Millisecond
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := db.QueryRow(query, jobID).Scan(dest...)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		if err != sql.ErrNoRows {
			logger.Warnf("Lookup for job %s failed (will retry): %v", jobID, err)
		}
		time.Sleep(delay)
		if delay < 8*time.Second {
			delay *= 2
		}
	}
}

func handleStaticSiteMessage(payload string) bool {
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		logger.Errorf("Error unmarshalling event payload: %v", err)
		return true
	}
	if ev.TargetService != "static-site" {
		logger.Infof("Skipping event for TargetService '%s' (Job ID: %s), not a static site event.", ev.TargetService, ev.JobID)
		return true
	}

	var siteID, status string
	err := lookupWithRetry("SELECT id, status FROM static_sites WHERE sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", ev.JobID.String(), &siteID, &status)
	if err != nil {
		logger.Errorf("[SFSP JobID: %s] Could not find a matching static_site record in app-db: %v", ev.JobID, err)
		return true
	}

	// 再配送（Ack 前の再起動など）で、既に終端状態の行を上書きしない
	if status != "scanning" && status != "processing" {
		logger.Infof("[SiteID: %s] Site is already in terminal status '%s'; skipping event.", siteID, status)
		return true
	}

	if ev.FinalStatus != "clean" {
		logger.Infof("[SiteID: %s] Scan result is '%s'. Aborting processing.", siteID, ev.FinalStatus)
		finalStatus := ev.FinalStatus
		if finalStatus == "malicious" || finalStatus == "suspicious" {
			finalStatus = "quarantined"
		}
		return updateProcessingStatus(siteID, finalStatus, fmt.Sprintf("File scan failed with status: %s", ev.FinalStatus)) == nil
	}

	var result error
	func() {
		defer func() {
			if r := recover(); r != nil {
				result = fmt.Errorf("panic: %v", r)
				_ = updateProcessingStatus(siteID, "failed", "Internal error while processing the site")
			}
		}()
		result = processStaticSiteJob(siteID, ev)
	}()
	// result は終端状態の書き込みに失敗した場合のみ non-nil
	return result == nil
}

func updateProcessingStatus(siteID, status, details string) error {
	_, err := db.Exec("UPDATE static_sites SET status = $1, processing_details = $2 WHERE id = $3", status, details, siteID)
	if err != nil {
		logger.Errorf("Failed to update processing details for SiteID %s: %v", siteID, err)
	}
	return err
}

// downloadCleanFile は clean-files 上のオブジェクト（キーは sfsp が保存する <file_id>）を f に書き込み、
// 取得したキーを返す。limit を超えるオブジェクトは errLimitExceeded を返す。
func downloadCleanFile(ctx context.Context, logPrefix string, ev event.ScanCompletionEvent, f *os.File, limit int64) (string, error) {
	key := ev.FileID.String()
	obj, err := sfspMinioClient.GetObject(ctx, sfspBucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s: %w", key, err)
	}
	defer obj.Close()
	n, err := io.CopyN(f, obj, limit+1)
	if err == io.EOF {
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", key, err)
	}
	if n > limit {
		return "", errLimitExceeded
	}
	logger.Infof("%s Successfully downloaded %s from SFSP", logPrefix, key)
	return key, nil
}

// removeSitePrefix は失敗時に、途中までアップロードしたサイトのファイルを削除する。
func removeSitePrefix(ctx context.Context, siteID string) {
	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		for object := range minioClient.ListObjects(ctx, minioBucket, minio.ListObjectsOptions{Prefix: siteID + "/", Recursive: true}) {
			if object.Err != nil {
				logger.Warnf("[SiteID: %s] Failed to list objects for cleanup: %v", siteID, object.Err)
				return
			}
			objectsCh <- object
		}
	}()
	for rErr := range minioClient.RemoveObjects(ctx, minioBucket, objectsCh, minio.RemoveObjectsOptions{}) {
		logger.Warnf("[SiteID: %s] Failed to remove %s during cleanup: %v", siteID, rErr.ObjectName, rErr.Err)
	}
}

// limitedReader は n バイトを超えて読もうとしたら errLimitExceeded を返す。
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// 上限ちょうどで終わっているかを 1 バイト読んで確認する
		var b [1]byte
		n, err := l.r.Read(b[:])
		if n > 0 {
			return 0, errLimitExceeded
		}
		return 0, err
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func contentTypeFor(relPath string) string {
	lowerPath := strings.ToLower(relPath)
	switch {
	case strings.HasSuffix(lowerPath, ".html"), strings.HasSuffix(lowerPath, ".htm"):
		return "text/html"
	case strings.HasSuffix(lowerPath, ".css"):
		return "text/css"
	case strings.HasSuffix(lowerPath, ".js"):
		return "application/javascript"
	case strings.HasSuffix(lowerPath, ".png"):
		return "image/png"
	case strings.HasSuffix(lowerPath, ".jpg"), strings.HasSuffix(lowerPath, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lowerPath, ".svg"):
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// processStaticSiteJob は clean な zip を取得・検証・展開して公開する。
// 失敗した場合も status を終端状態（error / failed）にする。
// 戻り値は終端状態を DB に書き込めなかった場合のみ non-nil（呼び出し元は Ack しない）。
func processStaticSiteJob(siteID string, ev event.ScanCompletionEvent) error {
	logger.Debugf("processStaticSiteJob started. siteID=%s jobID=%s file=%s", siteID, ev.JobID, ev.Filename)
	ctx := context.Background()
	logPrefix := fmt.Sprintf("[SiteID: %s]", siteID)

	fail := func(status, details string) error {
		logger.Errorf("%s %s", logPrefix, details)
		return updateProcessingStatus(siteID, status, details)
	}

	tmpZip, err := os.CreateTemp("", "static-site-*.zip")
	if err != nil {
		return fail("failed", fmt.Sprintf("Failed to create temp file: %v", err))
	}
	tempZipPath := tmpZip.Name()
	defer os.Remove(tempZipPath)
	defer tmpZip.Close()

	_ = updateProcessingStatus(siteID, "processing", "クリーンなファイルをダウンロード中...")

	downloadedKey, err := downloadCleanFile(ctx, logPrefix, ev, tmpZip, maxSiteUploadBytes)
	if err == errLimitExceeded {
		return fail("failed", fmt.Sprintf("ZIP file exceeds the maximum size of %d bytes", maxSiteUploadBytes))
	}
	if err != nil {
		return fail("error", fmt.Sprintf("Failed to download clean file from SFSP MinIO: %v", err))
	}
	if err := tmpZip.Close(); err != nil {
		return fail("failed", fmt.Sprintf("Failed to write temp file: %v", err))
	}

	_ = updateProcessingStatus(siteID, "processing", "ZIPファイルを解凍中...")
	zipReader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		return fail("error", fmt.Sprintf("Failed to open zip file: %v", err))
	}
	defer zipReader.Close()

	// --- zip bomb 対策: エントリ数・宣言サイズの事前検査 ---
	if int64(len(zipReader.File)) > siteMaxZipEntries {
		return fail("failed", fmt.Sprintf("ZIP contains too many entries (%d > %d)", len(zipReader.File), siteMaxZipEntries))
	}
	var declaredTotal uint64
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > uint64(siteMaxEntryBytes) {
			return fail("failed", fmt.Sprintf("ZIP entry %q exceeds the per-file limit of %d bytes", file.Name, siteMaxEntryBytes))
		}
		declaredTotal += file.UncompressedSize64
		if declaredTotal > uint64(siteMaxExtractedBytes) {
			return fail("failed", fmt.Sprintf("ZIP extracted size exceeds the limit of %d bytes", siteMaxExtractedBytes))
		}
	}

	var indexZipPath string
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		normalizedPath := strings.ReplaceAll(file.Name, "\\", "/")
		if strings.ToLower(path.Base(normalizedPath)) == "index.html" {
			indexZipPath = normalizedPath
			break
		}
	}
	if indexZipPath == "" {
		return fail("error", "index.html not found in the ZIP file")
	}

	zipRootDir := path.Dir(indexZipPath)
	if zipRootDir == "." {
		zipRootDir = ""
	} else {
		zipRootDir = zipRootDir + "/"
	}

	// Zip Slip 対策は全エントリをアップロード前に検査する（途中まで公開されるのを防ぐ）
	type entry struct {
		file    *zip.File
		relPath string
	}
	entries := make([]entry, 0, len(zipReader.File))
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fail("error", fmt.Sprintf("Illegal file path in ZIP: %s", file.Name))
		}
		normalizedPath := strings.ReplaceAll(file.Name, "\\", "/")
		relPath := normalizedPath
		if zipRootDir != "" && strings.HasPrefix(normalizedPath, zipRootDir) {
			relPath = strings.TrimPrefix(normalizedPath, zipRootDir)
		}
		// path.Join は ".." を解決するため、サイト外（他サイトや thumbnails/）を
		// 指すエントリを含む ZIP は拒否する
		cleanRelPath := path.Clean(relPath)
		if path.IsAbs(relPath) || strings.ContainsRune(relPath, 0) || cleanRelPath == "." || cleanRelPath == ".." || strings.HasPrefix(cleanRelPath, "../") {
			return fail("error", fmt.Sprintf("Illegal file path in ZIP: %s", file.Name))
		}
		entries = append(entries, entry{file: file, relPath: cleanRelPath})
	}

	_ = updateProcessingStatus(siteID, "processing", "ファイルをアップロード中...")
	remaining := siteMaxExtractedBytes
	for _, e := range entries {
		declared := int64(e.file.UncompressedSize64)
		limit := siteMaxEntryBytes
		if remaining < limit {
			limit = remaining
		}
		if declared > limit {
			removeSitePrefix(ctx, siteID)
			return fail("failed", fmt.Sprintf("ZIP extracted size exceeds the limit of %d bytes", siteMaxExtractedBytes))
		}

		rc, err := e.file.Open()
		if err != nil {
			removeSitePrefix(ctx, siteID)
			return fail("error", fmt.Sprintf("Failed to open file in zip: %v", err))
		}
		// 宣言サイズを信用せず、実際に読んだバイト数でも上限を強制する
		lr := &limitedReader{r: rc, n: limit}
		minioPath := path.Join(siteID, e.relPath)
		_, err = minioClient.PutObject(ctx, minioBucket, minioPath, lr, declared, minio.PutObjectOptions{ContentType: contentTypeFor(e.relPath)})
		rc.Close()
		if err != nil {
			removeSitePrefix(ctx, siteID)
			if errors.Is(err, errLimitExceeded) || strings.Contains(err.Error(), errLimitExceeded.Error()) {
				return fail("failed", fmt.Sprintf("ZIP entry %q exceeds the size limit", e.file.Name))
			}
			return fail("error", fmt.Sprintf("Failed to upload file to MinIO: %v", err))
		}
		remaining -= declared
	}

	// 1. DB ステータスを 'public' に更新
	entryPointPath := "index.html"
	if _, err = db.Exec("UPDATE static_sites SET status = 'public', entry_point_path = $1, processing_details = NULL WHERE id = $2", entryPointPath, siteID); err != nil {
		return fail("error", fmt.Sprintf("Failed to update static site status to public: %v", err))
	}

	// 2. 本番化成功後に SFSP MinIO (clean-files) からファイルを削除
	if downloadedKey != "" {
		if err := sfspMinioClient.RemoveObject(ctx, sfspBucketName, downloadedKey, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("%s Failed to delete clean file from SFSP MinIO (%s): %v", logPrefix, downloadedKey, err)
		} else {
			logger.Infof("%s Successfully deleted clean file from SFSP MinIO (%s)", logPrefix, downloadedKey)
		}
	}

	logger.Infof("Successfully processed static site job for SiteID: %s", siteID)
	return nil
}

func handleThumbnailMessage(payload string) bool {
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		logger.Errorf("Error unmarshalling thumbnail event payload: %v", err)
		return true
	}
	if ev.TargetService != "site-thumbnail" {
		logger.Infof("Skipping event for TargetService '%s' (Job ID: %s), not a site thumbnail event.", ev.TargetService, ev.JobID)
		return true
	}
	return processStaticSiteThumbnail(context.Background(), &ev)
}

// clearThumbnailJob はサムネイルのジョブを終了させる（フロントエンドのポーリングが「却下」と判定できる）。
// 新しいジョブに置き換わっている場合は何もしない。
func clearThumbnailJob(siteID string, jobID string) bool {
	_, err := db.Exec("UPDATE static_sites SET thumbnail_sfsp_job_id = NULL WHERE id = $1 AND thumbnail_sfsp_job_id = $2", siteID, jobID)
	if err != nil {
		logger.Errorf("[SiteID: %s] Failed to clear thumbnail scan job: %v", siteID, err)
		return false
	}
	return true
}

// thumbnailKeyFromURL は "/static-sites/thumbnails/xxx.ext" を MinIO のキー "thumbnails/xxx.ext" にする。
func thumbnailKeyFromURL(u string) string {
	const prefix = "/static-sites/"
	if !strings.HasPrefix(u, prefix) {
		return ""
	}
	key := strings.TrimPrefix(u, prefix)
	if !strings.HasPrefix(key, "thumbnails/") || strings.Contains(key, "..") || strings.Count(key, "/") != 1 {
		return ""
	}
	return key
}

// processStaticSiteThumbnail の戻り値は Ack してよいか（終端状態を DB に反映できたか）。
func processStaticSiteThumbnail(ctx context.Context, ev *event.ScanCompletionEvent) bool {
	jobID := ev.JobID.String()
	var siteID string
	err := lookupWithRetry("SELECT id FROM static_sites WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", jobID, &siteID)
	if err != nil {
		logger.Errorf("[Thumbnail JobID: %s] Could not find a matching static_site record: %v", jobID, err)
		return true
	}
	logPrefix := fmt.Sprintf("[SiteID: %s]", siteID)
	logger.Infof("%s Found matching static site record for thumbnail SFSP JobID %s", logPrefix, jobID)

	if ev.FinalStatus != "clean" {
		logger.Infof("%s Thumbnail scan result is '%s'. Aborting thumbnail processing.", logPrefix, ev.FinalStatus)
		return clearThumbnailJob(siteID, jobID)
	}

	tmp, err := os.CreateTemp("", "site-thumb-*")
	if err != nil {
		logger.Errorf("%s Failed to create temp file for thumbnail: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	downloadedKey, err := downloadCleanFile(ctx, logPrefix, *ev, tmp, maxImageUploadBytes)
	if err != nil {
		logger.Errorf("%s Download thumbnail from SFSP failed: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("%s Failed to reset thumbnail file: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}

	buffer := make([]byte, 512)
	n, _ := io.ReadFull(tmp, buffer)
	contentType := http.DetectContentType(buffer[:n])
	ext, ok := imageExtensions[contentType]
	if !ok {
		logger.Errorf("%s Rejected thumbnail with non-image content type %q", logPrefix, contentType)
		return clearThumbnailJob(siteID, jobID)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("%s Failed to reset thumbnail file: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}
	stat, err := tmp.Stat()
	if err != nil {
		logger.Errorf("%s Failed to stat thumbnail file: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}

	thumbnailRandomName := generateRandomThumbnailName()
	if thumbnailRandomName == "" {
		return clearThumbnailJob(siteID, jobID)
	}
	newKey := fmt.Sprintf("thumbnails/%s%s", thumbnailRandomName, ext)
	if _, err = minioClient.PutObject(ctx, minioBucket, newKey, tmp, stat.Size(), minio.PutObjectOptions{ContentType: contentType}); err != nil {
		logger.Errorf("%s Failed to upload thumbnail to static-site MinIO: %v", logPrefix, err)
		return clearThumbnailJob(siteID, jobID)
	}
	thumbnailURL := "/static-sites/" + newKey

	// 新しい URL を確定させる。ジョブが別のアップロードに置き換わっていたら反映しない。
	oldURL, committed, err := commitThumbnail(ctx, siteID, jobID, thumbnailURL)
	if err != nil {
		logger.Errorf("%s Failed to update thumbnail URL in DB: %v", logPrefix, err)
		_ = minioClient.RemoveObject(ctx, minioBucket, newKey, minio.RemoveObjectOptions{})
		// DB に書けていないので Ack しない（再起動時に再処理）
		return false
	}
	if !committed {
		logger.Infof("%s Thumbnail job %s was superseded; discarding uploaded object", logPrefix, jobID)
		_ = minioClient.RemoveObject(ctx, minioBucket, newKey, minio.RemoveObjectOptions{})
		return true
	}

	// 新しいサムネイルの確定後に古いオブジェクトを削除する（このサイトのもので、他から参照されていない場合のみ）
	if oldKey := thumbnailKeyFromURL(oldURL); oldKey != "" && oldKey != newKey {
		var others int
		if err := db.QueryRow("SELECT COUNT(*) FROM static_sites WHERE thumbnail_url = $1 AND id <> $2", oldURL, siteID).Scan(&others); err != nil {
			logger.Warnf("%s Failed to check references to old thumbnail %s: %v", logPrefix, oldKey, err)
		} else if others == 0 {
			if err := minioClient.RemoveObject(ctx, minioBucket, oldKey, minio.RemoveObjectOptions{}); err != nil {
				logger.Warnf("%s Failed to delete old thumbnail %s: %v", logPrefix, oldKey, err)
			} else {
				logger.Infof("%s Deleted old thumbnail %s", logPrefix, oldKey)
			}
		}
	}

	if downloadedKey != "" {
		if err := sfspMinioClient.RemoveObject(ctx, sfspBucketName, downloadedKey, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("%s Failed to delete clean thumbnail from SFSP MinIO (%s): %v", logPrefix, downloadedKey, err)
		}
	}

	logger.Infof("%s Thumbnail processing complete.", logPrefix)
	return true
}

// commitThumbnail は thumbnail_sfsp_job_id が jobID のままの場合に限り thumbnail_url を差し替え、
// 差し替え前の URL を返す。
func commitThumbnail(ctx context.Context, siteID, jobID, newURL string) (string, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var oldURL sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT thumbnail_url FROM static_sites WHERE id = $1 AND thumbnail_sfsp_job_id = $2 FOR UPDATE", siteID, jobID).Scan(&oldURL)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE static_sites SET thumbnail_url = $1, thumbnail_sfsp_job_id = NULL WHERE id = $2", newURL, siteID); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return oldURL.String, true, nil
}
