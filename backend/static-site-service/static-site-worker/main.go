// static-site-worker は、SFSP（ファイルスキャン基盤）のスキャン完了イベントを Redis キューから受け取り、
// 静的サイトの ZIP を検証・展開して MinIO に公開する処理と、サイトのサムネイル画像を保存する処理を行う。
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

// ワーカー全体で共有する接続・設定。main で初期化する。
//   - db: アプリ用 PostgreSQL への接続
//   - minioClient / minioBucket: 公開用（静的サイト配信用）MinIO のクライアントとバケット名（MINIO_BUCKET_NAME）
//   - sfspMinioClient / sfspBucketName: スキャンで clean と判定されたファイルが置かれる SFSP 側 MinIO のクライアントとバケット名
//   - logger: 構造化ロガー
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

// workerConcurrency は処理を同時に走らせる最大数（静的サイト・サムネイル合計）。
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

// envInt64 は環境変数 name を正の int64 として読み取る。
// 未設定なら def を返し、数値でない・0 以下の場合は警告を出して def を返す。
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

// main は DB・MinIO（公開用と SFSP 用）・Redis へ接続し、
// 静的サイト用とサムネイル用の 2 つのキューのコンシューマを起動して、そのまま待ち続ける。
func main() {
	// 1. ロガーを初期化する
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	// 2. サイズ上限を環境変数で上書きする（logger を使うのでロガー初期化後に行う）
	maxSiteUploadBytes = envInt64("MAX_SITE_UPLOAD_BYTES", maxSiteUploadBytes)
	maxImageUploadBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", maxImageUploadBytes)
	siteMaxExtractedBytes = envInt64("SITE_MAX_EXTRACTED_BYTES", siteMaxExtractedBytes)
	siteMaxZipEntries = envInt64("SITE_MAX_ZIP_ENTRIES", siteMaxZipEntries)
	siteMaxEntryBytes = envInt64("SITE_MAX_ENTRY_BYTES", siteMaxEntryBytes)

	// 3. 共通設定（Redis 接続情報など）を読み込む
	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// 4. PostgreSQL に接続する（起動直後は DB が未準備のことがあるため 5 秒間隔で最大 5 回試す）
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

	// 5. 公開用 MinIO に接続する（ListBuckets で疎通を確認し、失敗時は同様にリトライする）
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

	// 6. SFSP 側 MinIO のクライアントを作る（minio.New は通信しないため、ここでは疎通確認をしていない）
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

	// 7. Redis に接続し、購読するキューとハンドラの組を定義する
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

	// 8. 2 つのキューのコンシューマを起動する。sem は両キューで共有し、同時処理数を workerConcurrency に抑える
	sem := make(chan struct{}, workerConcurrency)
	for _, q := range queues {
		go consume(ctx, q.name, q.handler, sem)
	}
	// main を終了させずに待ち続ける
	select {}
}

// consume は queueName から信頼性のある取り出し（BRPOPLPUSH）を行い、handler を実行する。
// handler が true を返したときだけ Ack する（false = 終端状態を DB に書けなかったので、
// 次回起動時の RecoverProcessing で再処理させる）。
func consume(ctx context.Context, queueName string, handler func(string) bool, sem chan struct{}) {
	for {
		// 1. メッセージを処理中リストへ移しながら取り出す（5 秒でタイムアウトし、空なら redis.Nil）
		payload, err := queue.DequeueReliable(ctx, queueName, 5*time.Second)
		if err == redis.Nil {
			continue
		}
		if err != nil {
			logger.Errorf("Error popping job from Redis (%s): %v", queueName, err)
			time.Sleep(5 * time.Second)
			continue
		}

		// 2. セマフォの空きを待ってから、別ゴルーチンでハンドラを実行する
		sem <- struct{}{}
		go func(payload string) {
			defer func() { <-sem }()
			ack := true
			func() {
				// ハンドラが panic した場合は Ack する扱いにする（同じメッセージで再起動のたびに panic し続けるのを防ぐため）
				defer func() {
					if r := recover(); r != nil {
						logger.Errorf("Panic while handling message from %s: %v", queueName, r)
						ack = true
					}
				}()
				ack = handler(payload)
			}()
			// 3. ハンドラが false を返したら処理中リストに残し、成功時のみ Ack（処理中リストから削除）する
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

// handleStaticSiteMessage は静的サイト用キューのスキャン完了イベントを処理する。
// 戻り値は Ack してよいか。解析できない・対象外・該当レコードなし・処理済みのイベントは true を返して捨てる。
func handleStaticSiteMessage(payload string) bool {
	// 1. イベントを解析し、静的サイト宛てのものだけを対象にする
	var ev event.ScanCompletionEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		logger.Errorf("Error unmarshalling event payload: %v", err)
		return true
	}
	if ev.TargetService != "static-site" {
		logger.Infof("Skipping event for TargetService '%s' (Job ID: %s), not a static site event.", ev.TargetService, ev.JobID)
		return true
	}

	// 2. SFSP のジョブ ID から対象サイトの行を探す
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

	// 3. スキャン結果が clean 以外なら公開せずに終える（malicious / suspicious は quarantined として記録する）
	if ev.FinalStatus != "clean" {
		logger.Infof("[SiteID: %s] Scan result is '%s'. Aborting processing.", siteID, ev.FinalStatus)
		finalStatus := ev.FinalStatus
		if finalStatus == "malicious" || finalStatus == "suspicious" {
			finalStatus = "quarantined"
		}
		return updateProcessingStatus(siteID, finalStatus, fmt.Sprintf("File scan failed with status: %s", ev.FinalStatus)) == nil
	}

	// 4. 展開・公開処理を行う。panic した場合はサイトを failed にし、result を non-nil にする
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
	// result は終端状態の書き込みに失敗した場合と panic した場合に non-nil
	return result == nil
}

// updateProcessingStatus は static_sites の status と processing_details（進捗・エラー内容の表示用）を更新する。
// 失敗時はログを出してエラーを返す。
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
	// limit+1 バイトまで読み、limit を超えて読めたら上限超過と判定する（EOF は正常終了扱い）
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
// 公開用バケットの "<siteID>/" 配下を列挙して一括削除する。削除できなかったものは警告ログのみ出す。
func removeSitePrefix(ctx context.Context, siteID string) {
	// 列挙結果を別ゴルーチンからチャネルへ流し、RemoveObjects に渡す（列挙エラー時はそこで打ち切る）
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

// Read は残り許容量 n を超えないように p を切り詰めて読み、読んだ分だけ n を減らす。
// io.LimitReader と違い、上限を超えるデータがあれば EOF ではなくエラーにする。
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

// contentTypeFor は拡張子（大文字小文字を区別しない）から公開時の Content-Type を決める。
// 対応表にない拡張子は application/octet-stream にする。
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

	// fail はエラーをログに出し、サイトを終端状態 status にする。戻り値は DB 更新のエラー。
	fail := func(status, details string) error {
		logger.Errorf("%s %s", logPrefix, details)
		return updateProcessingStatus(siteID, status, details)
	}

	// 1. clean-files から ZIP を一時ファイルへダウンロードする（サイズ上限付き）
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

	// 2. ZIP を開く
	_ = updateProcessingStatus(siteID, "processing", "ZIPファイルを解凍中...")
	zipReader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		return fail("error", fmt.Sprintf("Failed to open zip file: %v", err))
	}
	defer zipReader.Close()

	// 3. zip bomb 対策: エントリ数・宣言サイズの事前検査（展開前にヘッダの値だけで弾く）
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

	// 4. 最初に見つかった index.html（大文字小文字を区別しない）の場所をサイトのルートとみなす
	//    （ZIP 内の格納順で最初のものを採用するため、階層の浅いものが優先されるとは限らない）
	var indexZipPath string
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		// バックスラッシュ区切りのパスも扱えるよう "/" にそろえる
		normalizedPath := strings.ReplaceAll(file.Name, "\\", "/")
		if strings.ToLower(path.Base(normalizedPath)) == "index.html" {
			indexZipPath = normalizedPath
			break
		}
	}
	if indexZipPath == "" {
		return fail("error", "index.html not found in the ZIP file")
	}

	// index.html のあるディレクトリ（末尾 "/" 付き、ZIP 直下なら空文字）を、公開時に取り除く接頭辞にする
	zipRootDir := path.Dir(indexZipPath)
	if zipRootDir == "." {
		zipRootDir = ""
	} else {
		zipRootDir = zipRootDir + "/"
	}

	// 5. Zip Slip 対策は全エントリをアップロード前に検査する（途中まで公開されるのを防ぐ）
	//    シンボリックリンクと不正なパスがあれば ZIP 全体を拒否し、公開先の相対パスを決める
	// entry は検査済みの ZIP エントリと、公開先（<siteID>/ 配下）の相対パスの組。
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
		// ルート配下のエントリは接頭辞を外す。ルート外のエントリは ZIP 内のパスのまま公開される
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

	// 6. 各エントリを公開用 MinIO の "<siteID>/<relPath>" にアップロードする。
	//    remaining は展開後合計の残り許容量。途中で失敗したらアップロード済みのファイルを削除する
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
			// 上限超過かどうかは errors.Is に加え、エラーメッセージに含まれる文字列でも判定する（minio-go 経由で包まれて返る場合に備える）
			if errors.Is(err, errLimitExceeded) || strings.Contains(err.Error(), errLimitExceeded.Error()) {
				return fail("failed", fmt.Sprintf("ZIP entry %q exceeds the size limit", e.file.Name))
			}
			return fail("error", fmt.Sprintf("Failed to upload file to MinIO: %v", err))
		}
		remaining -= declared
	}

	// 7. DB ステータスを 'public' に更新（エントリポイントは常に "index.html"）
	entryPointPath := "index.html"
	if _, err = db.Exec("UPDATE static_sites SET status = 'public', entry_point_path = $1, processing_details = NULL WHERE id = $2", entryPointPath, siteID); err != nil {
		return fail("error", fmt.Sprintf("Failed to update static site status to public: %v", err))
	}

	// 8. 本番化成功後に SFSP MinIO (clean-files) からファイルを削除（失敗しても警告のみ）
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

// handleThumbnailMessage はサムネイル用キューのスキャン完了イベントを解析し、
// サイトのサムネイル宛てのものだけを processStaticSiteThumbnail に渡す。戻り値は Ack してよいか。
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
// 新しいジョブに置き換わっている場合は何もしない。戻り値は DB 更新に成功したか（= Ack してよいか）。
func clearThumbnailJob(siteID string, jobID string) bool {
	_, err := db.Exec("UPDATE static_sites SET thumbnail_sfsp_job_id = NULL WHERE id = $1 AND thumbnail_sfsp_job_id = $2", siteID, jobID)
	if err != nil {
		logger.Errorf("[SiteID: %s] Failed to clear thumbnail scan job: %v", siteID, err)
		return false
	}
	return true
}

// thumbnailKeyFromURL は "/static-sites/thumbnails/xxx.ext" を MinIO のキー "thumbnails/xxx.ext" にする。
// 形式が合わない URL には空文字を返す。
func thumbnailKeyFromURL(u string) string {
	const prefix = "/static-sites/"
	if !strings.HasPrefix(u, prefix) {
		return ""
	}
	key := strings.TrimPrefix(u, prefix)
	// 削除対象を thumbnails/ 直下の 1 オブジェクトに限定し、サイト本体など他のキーを消さないようにする
	if !strings.HasPrefix(key, "thumbnails/") || strings.Contains(key, "..") || strings.Count(key, "/") != 1 {
		return ""
	}
	return key
}

// processStaticSiteThumbnail はスキャン済みのサムネイル画像を検証して公開用 MinIO に保存し、
// サイトの thumbnail_url を差し替える。途中で失敗した場合はジョブを終了させる（clearThumbnailJob）。
// 戻り値は Ack してよいか（終端状態を DB に反映できたか）。
func processStaticSiteThumbnail(ctx context.Context, ev *event.ScanCompletionEvent) bool {
	// 1. サムネイル用のジョブ ID から対象サイトを探す
	jobID := ev.JobID.String()
	var siteID string
	err := lookupWithRetry("SELECT id FROM static_sites WHERE thumbnail_sfsp_job_id = $1 ORDER BY created_at DESC LIMIT 1", jobID, &siteID)
	if err != nil {
		logger.Errorf("[Thumbnail JobID: %s] Could not find a matching static_site record: %v", jobID, err)
		return true
	}
	logPrefix := fmt.Sprintf("[SiteID: %s]", siteID)
	logger.Infof("%s Found matching static site record for thumbnail SFSP JobID %s", logPrefix, jobID)

	// 2. スキャン結果が clean 以外なら保存せずにジョブを終了させる
	if ev.FinalStatus != "clean" {
		logger.Infof("%s Thumbnail scan result is '%s'. Aborting thumbnail processing.", logPrefix, ev.FinalStatus)
		return clearThumbnailJob(siteID, jobID)
	}

	// 3. clean-files から画像を一時ファイルへダウンロードする（サイズ上限付き）
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

	// 4. 先頭 512 バイトの中身から Content-Type を判定し、許可した画像形式以外は拒否する
	//    （申告されたファイル名や Content-Type は信用しない）
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

	// 5. ランダムな名前で公開用 MinIO の thumbnails/ にアップロードする
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

	// 6. 新しい URL を確定させる。ジョブが別のアップロードに置き換わっていたら反映しない。
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

	// 7. 新しいサムネイルの確定後に古いオブジェクトを削除する（このサイトのもので、他から参照されていない場合のみ）
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

	// 8. SFSP MinIO (clean-files) の元ファイルを削除する（失敗しても警告のみ）
	if downloadedKey != "" {
		if err := sfspMinioClient.RemoveObject(ctx, sfspBucketName, downloadedKey, minio.RemoveObjectOptions{}); err != nil {
			logger.Warnf("%s Failed to delete clean thumbnail from SFSP MinIO (%s): %v", logPrefix, downloadedKey, err)
		}
	}

	logger.Infof("%s Thumbnail processing complete.", logPrefix)
	return true
}

// commitThumbnail は thumbnail_sfsp_job_id が jobID のままの場合に限り thumbnail_url を差し替え、
// 差し替え前の URL を返す。ジョブが置き換わっていた（行がない）場合は committed=false を返す。
// 行を FOR UPDATE でロックし、同じサイトへの並行した差し替えと競合しないようにしている。
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
