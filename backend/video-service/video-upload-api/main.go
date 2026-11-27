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
	"github.com/atmosidea/shared/ratelimit"
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

// video-upload-api は動画のアップロードを受け付け、SFSP（ファイルスキャン）に送ったうえで、
// スキャン完了イベントを受けて HLS へ変換し、動画用 MinIO へ配置するサービス。
//
// 以下はプロセス全体で共有する設定値・接続・状態。main で環境変数から初期化する。
var (
	// db は app-db（PostgreSQL）への接続プール。
	db *pgxpool.Pool
	// jwtSecret は JWT（HS256）の検証に使う共有鍵。
	jwtSecret []byte
	// uploadDir は変換ジョブの作業ディレクトリを作る親ディレクトリ（UPLOAD_DIR）。
	uploadDir string
	// sfspApiUrl / sfspApiKey は sfsp-api の URL と API キー。
	sfspApiUrl string
	sfspApiKey string
	// sfspCleanMinioClient / sfspCleanBucketName はスキャン済み（クリーン）ファイルを置く SFSP 側 MinIO とそのバケット。
	sfspCleanMinioClient *minio.Client
	sfspCleanBucketName  string
	// videoMinioClient / videoBucketName は HLS とサムネイルの配信元になる動画用 MinIO とそのバケット。
	videoMinioClient *minio.Client
	videoBucketName  = "videos"
	// maxVideoUploadBytes は動画ファイル本体の上限（MAX_VIDEO_UPLOAD_BYTES、既定 1 GiB）。
	maxVideoUploadBytes = int64(1 << 30)
	// ffmpegSem は同時に実行する変換ジョブ（= ffmpeg）の数を FFMPEG_MAX_CONCURRENCY に制限する。
	ffmpegSem chan struct{}
	// ffmpegTimeout は ffmpeg 1 回の実行に許す最大時間（FFMPEG_TIMEOUT、既定 2h）。
	ffmpegTimeout = 2 * time.Hour
	logger        *zap.SugaredLogger
	// allowedVideoMimeTypes は先頭 512 バイトから判定した MIME タイプのうち、受け付けるもの。
	allowedVideoMimeTypes = map[string]bool{
		"video/mp4":        true,
		"video/webm":       true,
		"video/quicktime":  true,
		"video/x-msvideo":  true,
		"video/x-matroska": true,
	}
	// teamServiceURL はチーム投稿権限を問い合わせる team-service のベース URL（末尾の / は除去済み）。
	teamServiceURL string

	// jobs は処理中の変換ジョブのキャンセル関数（動画 ID がキー）。削除ハンドラーが呼ぶ。
	jobsMu sync.Mutex
	jobs   = map[string]context.CancelFunc{}
)

// multipartOverhead は動画ファイル以外のフォーム（title / description / team_id と境界）に許す余裕。
const multipartOverhead = 1 << 20

// teamAccessCheck は team-service の権限エンドポイントに問い合わせ、呼び出し元が指定チームへ投稿できるかを確認する。
// 投稿できる場合に true を返す。team_id が空なら全体公開の投稿として常に許可する。
// 通信失敗・200 以外の応答・応答の解析失敗はすべて拒否（false）として扱う（fail closed）。
func teamAccessCheck(c *gin.Context, teamToken string) bool {
	if teamToken == "" {
		return true
	}
	// 呼び出し元の Authorization ヘッダーをそのまま転送し、本人の権限で判定させる。
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

// videos.status に入る動画の状態。scanning と processing 以外は終端状態として扱う。
const (
	// StatusScanning は SFSP のスキャン結果待ち。
	StatusScanning = "scanning"
	// StatusProcessing はスキャン済みで HLS 変換の待機中・実行中。
	StatusProcessing = "processing"
	// StatusPublic は変換が完了し公開済み。
	StatusPublic = "public"
	// StatusError は変換などの処理に失敗した状態。
	StatusError = "error"
	// StatusQuarantined はスキャンで malicious / suspicious と判定され隔離された状態。
	StatusQuarantined = "quarantined"
)

// SfspUploadResponse は sfsp-api の POST /api/v1/files の応答本文。
type SfspUploadResponse struct {
	FileID string `json:"file_id"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	SHA256 string `json:"sha256"`
}

// generateRandomID は暗号論的乱数 5 バイトから 10 文字の 16 進文字列（videos.id 用）を生成する。
func generateRandomID() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// envInt64 は環境変数を int64 として読み、未設定・不正・0 以下なら警告を出して既定値を返す。
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

// envDuration は環境変数を time.Duration（例: "90m"）として読み、未設定・不正・0 以下なら警告を出して既定値を返す。
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

// safeExt は拡張子を英数字だけ（最大 10 文字）に制限し、小文字にして返す。条件を満たさなければ空文字。
// ユーザー由来のファイル名をローカルパスに混ぜないための処置（パストラバーサル対策）。
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

	// 書き込み側の goroutine。HTTP クライアントが pr を読むのに合わせて本文を生成し、
	// 終了時（エラー時を含む）に pw を閉じて読み取り側へ EOF またはエラーを伝える。
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
		// 読み取り側を閉じて、書き込み goroutine が pw への書き込みで止まったまま残らないようにする。
		pr.CloseWithError(err)
		return nil, 0, "", fmt.Errorf("failed to create SFSP request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setSFSPAuth(req)

	// 大きな動画の転送を見込み、タイムアウトは長め（30 分）に取る。
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		pr.CloseWithError(err)
		return nil, 0, "", fmt.Errorf("SFSP request failed: %w", err)
	}
	defer resp.Body.Close()

	// 応答本文は 1 MiB までに制限して読み、2xx 以外ならエラーとして本文ごと返す。
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

// ensureDatabaseExists は DATABASE_URL が指すデータベースが無ければ、管理用の postgres DB に接続して作成する。
// 対象が空または postgres の場合は何もしない。
func ensureDatabaseExists(databaseUrl string) error {
	config, err := pgxpool.ParseConfig(databaseUrl)
	if err != nil {
		return fmt.Errorf("failed to parse DATABASE_URL: %w", err)
	}

	targetDB := config.ConnConfig.Database
	if targetDB == "" || targetDB == "postgres" {
		return nil
	}

	// 同じホスト・認証情報で、既定の postgres データベースへ接続する DSN を組み立てる。
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
		// CREATE DATABASE はプレースホルダーを使えないため、識別子として " をエスケープして埋め込む。
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

// main は設定を読み込み、DB・Redis・2 つの MinIO に接続したうえで、
// スキャン完了イベントを処理するワーカーを起動し、:8080 で HTTP API を提供する。
func main() {
	// 1. ロガーを初期化する
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	// 2. 環境変数から設定を読み込む（必須項目が欠けていれば起動を中止する）
	ctx := context.Background()
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		logger.Fatal("JWT_SECRET environment variable is not set")
	}
	uploadDir = os.Getenv("UPLOAD_DIR")
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

	// アップロード上限と ffmpeg の同時実行数・タイムアウトを決める。
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

	// 共通設定（Redis 接続情報など）を読み込む。
	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// 3. データベースを用意して接続する（起動直後の DB 未準備に備え、それぞれ最大 5 回再試行する）
	// ここで失敗し続けても中止せず、続く接続処理の結果で判断する。
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

	// 4. Redis に接続する（イベントキューとトークン失効リストに使う）
	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	// 5. SFSP のクリーンファイル用 MinIO と、動画配信用 MinIO のクライアントを作る
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

	// 6. 前回の異常終了で Ack されずに残ったイベントを本来のキューへ戻してから消費を始める。
	if n, err := queue.RecoverProcessing(ctx, queue.StreamCompletionQueue); err != nil {
		logger.Errorf("Failed to recover unacknowledged events from %s: %v", queue.ProcessingQueueName(queue.StreamCompletionQueue), err)
	} else if n > 0 {
		logger.Warnf("Recovered %d unacknowledged event(s) back to %s", n, queue.StreamCompletionQueue)
	}

	go videoWorker(ctx)

	// 7. ルーティングを設定して HTTP サーバーを起動する（/api 配下はすべて JWT 認証が必要）
	// 保存量・ディスク・処理待ちの判定と、分割アップロードの準備
	initUploadGuard(context.Background(), maxVideoUploadBytes)

	// ユーザーごとのアップロード制限（UPLOAD_LIMIT_*、未設定なら既定値）
	uploadLimits := ratelimit.LoadUploadLimitsFromEnv()
	logger.Infof("Per-user upload limits: %d/hour, %d/day, %d concurrent", uploadLimits.PerHour, uploadLimits.PerDay, uploadLimits.Concurrent)
	r := gin.Default()
	r.MaxMultipartMemory = 32 << 20

	api := r.Group("/api")
	api.Use(authMiddleware())
	{
		api.POST("/videos/upload", uploadLimitMiddleware(uploadLimits), uploadHandler)
		// 分割アップロード（Cloudflare の 100 MB 制限への対応）
		registerChunkRoutes(api, "/videos/upload/chunks")
		api.DELETE("/videos/:id", deleteHandler)
		api.DELETE("/videos/:id/thumbnail", deleteThumbnailHandler)
	}

	logger.Info("INFO: Video Upload service starting on port 8080")
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（大きなアップロードを受けるので本文の読み取りは長めに許す）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

// uploadHandler は POST /api/videos/upload を処理する。動画を検証して SFSP へスキャン依頼し、
// videos に初期レコードを作って 202 Accepted を返す。変換はスキャン完了イベントを受けた videoWorker が行う。
func uploadHandler(c *gin.Context) {
	// 1. 認証済みユーザーの ID を取り出す
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

	// 2. サイズ上限を確認する（Content-Length で早期に弾き、本文自体も上限で打ち切る）
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

	// 3. 動画ファイルを取り出し、ファイル単体のサイズを確認する（分割アップロードなら upload_id から組み立て済みのファイルを開く）
	file, videoOrigName, videoSize, ok := openMainUpload(c, "video")
	if !ok {
		return
	}
	defer file.Close()

	if videoSize > maxVideoUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	// 保存量・ディスクの空き・処理待ち（検査・変換）を確かめる
	if !guardUploadSize(c, videoSize) {
		return
	}

	// 4. 先頭 512 バイトから実際の MIME タイプを判定する（クライアント申告の Content-Type は信用しない）
	// 判定後は SFSP へ全体を送るため、読み取り位置を先頭へ戻す。
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

	// 5. フォーム項目を取り出し、チーム指定があれば投稿権限を確認する
	title := c.PostForm("title")
	description := c.PostForm("description")
	teamID := c.PostForm("team_id")

	if !teamAccessCheck(c, teamID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to post to this team"})
		return
	}

	// 6. SFSP へファイルを送ってスキャンを依頼する（接続できない場合は 503、それ以外の失敗は 500）
	originalFilename := displayFilename(videoOrigName)

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

	// 7. 動画 ID を採番し、SFSP のジョブ ID を検証する
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

	// 8. 初期状態を決める。通常は scanning から始めるが、SFSP が 200 かつ status=clean を返した場合は
	// 既存のクリーンファイルとみなし、スキャン待ちを飛ばして processing から始める。
	videoStatus := StatusScanning
	processingDetails := "セキュリティスキャン待機中..."
	if sfspStatus == http.StatusOK && strings.ToLower(sfspRespData.Status) == "clean" {
		videoStatus = StatusProcessing
		processingDetails = "セキュリティスキャン完了 (既存クリーンファイル)。動画変換待機中..."
	}

	// 9. videos に初期レコードを作る。team_id が空なら NULL（全体公開）として保存する。
	teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}
	_, err = db.Exec(context.Background(),
		"INSERT INTO videos (id, title, description, filename, uploader_id, team_id, status, sfsp_job_id, processing_details, upload_bytes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
		videoID, title, description, originalFilename, uploaderUUID, teamIDVal, videoStatus, sfspJobID, processingDetails, videoSize)
	if err != nil {
		logger.Errorf("Error creating initial video record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video record"})
		return
	}

	// 10. 既存クリーンファイルの場合はスキャンが走らない想定のため、完了イベントをこちらで作り直して
	// キューに積み、videoWorker に変換させる。積めなかった場合はログのみで、応答は成功のまま返す。
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
// 取り出しエラー時は 5 秒待って再試行し、ctx が終了していれば戻る。
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

// ackStream は処理済みのイベントを処理中リストから取り除く。失敗してもログに残すだけにする。
func ackStream(payload string) {
	if err := queue.Ack(context.Background(), queue.StreamCompletionQueue, payload); err != nil {
		logger.Errorf("Failed to ack event on %s: %v", queue.StreamCompletionQueue, err)
	}
}

// findVideoByJob は sfsp_job_id に一致する動画を探す。スキャンが INSERT より先に終わる競合に備え、少し待って再試行する。
// 見つからない場合のみ 2 秒間隔で最大 10 回試し、それ以外のエラーは即座に返す。
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

// handleStreamEvent はスキャン完了イベント 1 件を処理する。処理を続けても意味がないイベント
// （壊れた JSON・対象外・該当動画なし・重複）は Ack して捨て、clean なら変換に進む。
// DB 更新に失敗した場合は Ack せず、次回起動時の回収に任せる。
func handleStreamEvent(payload string) {
	ctx := context.Background()

	// 1. イベントを解析し、動画向けか確認する
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

	// 2. 対応する動画レコードを探す
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

	// 3. 既に終端状態なら重複イベントとして捨てる
	if status != StatusScanning && status != StatusProcessing {
		logger.Infof("[VideoID: %s] Already in terminal status '%s'. Dropping duplicate event.", videoID, status)
		ackStream(payload)
		return
	}

	// 4. clean 以外ならスキャン結果を状態に記録して終える（malicious / suspicious は quarantined、それ以外は結果の文字列のまま）
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

	// 5. clean なら変換する。終端状態まで進めた場合（または動画が削除済みの場合）のみ Ack する。
	if processVideoAsync(videoID, ev.FileID.String(), ev.Filename) {
		ackStream(payload)
	}
}

// registerJob は動画 ID に対応する変換ジョブのキャンセル関数を登録簿に登録する。
func registerJob(videoID string, cancel context.CancelFunc) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	jobs[videoID] = cancel
}

// unregisterJob は変換ジョブの終了時に登録簿から取り除く。
func unregisterJob(videoID string) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	delete(jobs, videoID)
}

// cancelJob は処理中の変換ジョブがあればキャンセルして true を返す。無ければ false を返す。
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

	// 1. ffmpeg の同時実行枠を確保する（待機中に削除されたら後始末して終える）
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

	// 2. 作業ディレクトリを用意する
	// ユーザーのファイル名はローカルのパスに使わない。ジョブごとに一意な作業ディレクトリを作る。
	workDir, err := os.MkdirTemp(uploadDir, "job-"+videoID+"-")
	if err != nil {
		return fail(fmt.Sprintf("作業ディレクトリの作成に失敗しました: %v", err))
	}
	defer os.RemoveAll(workDir)

	tempVideoPath := filepath.Join(workDir, "input"+safeExt(filename))

	// 3. SFSP のクリーン用 MinIO から動画をダウンロードする
	_ = updateVideoStatus(bg, videoID, StatusProcessing, "クリーンな動画ファイルをダウンロード中...")

	// クリーン用バケット内のキー形式として考えられる候補を順に試し、最初に取得できたものを使う。
	// どれも無ければバケットを fileID の接頭辞で走査する。これを最大 5 回（5 秒間隔）繰り返す。
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

	// 4. HLS に変換する
	_ = updateVideoStatus(bg, videoID, StatusProcessing, "動画ファイルの変換を開始しました...")
	hlsOutputPath := filepath.Join(workDir, "hls")
	if err := os.MkdirAll(hlsOutputPath, os.ModePerm); err != nil {
		return fail(fmt.Sprintf("HLSディレクトリの作成に失敗しました: %v", err))
	}

	if err := convertToHLS(jobCtx, tempVideoPath, hlsOutputPath, videoID); err != nil {
		return fail(fmt.Sprintf("HLS変換に失敗しました: %v", err))
	}

	// 5. サムネイルを生成してアップロードする（失敗しても動画の公開は続ける）
	// 360p のプレイリストがあればそれを入力にし、無ければ元動画から切り出す。
	_ = updateVideoStatus(bg, videoID, StatusProcessing, "サムネイルを生成中...")
	thumbnailPath := ""
	generatedThumbnailPath := filepath.Join(workDir, "thumbnail.jpg")
	firstSegmentPath := filepath.Join(hlsOutputPath, "360p", "playlist.m3u8")

	thumbnailSource := firstSegmentPath
	if _, statErr := os.Stat(firstSegmentPath); os.IsNotExist(statErr) {
		thumbnailSource = tempVideoPath
	}
	if err := generateThumbnail(jobCtx, thumbnailSource, generatedThumbnailPath); err != nil {
		logger.Errorf("ERROR: Failed to generate thumbnail for video %s: %v", videoID, err)
	} else {
		thumbnailPath = uploadThumbnailToMinIO(jobCtx, videoID, generatedThumbnailPath)
	}

	// 6. 変換したHLSをMinIOへ展開する。ローカルの作業ディレクトリは defer で削除される。
	if err := uploadHLSMinIO(jobCtx, videoID, hlsOutputPath); err != nil {
		return fail(fmt.Sprintf("HLSのMinIOアップロードに失敗しました: %v", err))
	}

	// 変換・展開の間に削除されていたら、公開せずに後始末して終える。
	if deletedOrCancelled() {
		cleanupDeletedVideo(videoID)
		return true
	}

	// 7. 配信用パスとサムネイルを記録して public にする
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
		// 最終更新の直前に削除された。展開済みのオブジェクトを片付ける。
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
	// 停止理由を区別してエラーにする（タイムアウトか、呼び出し元のキャンセルか）。
	if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffmpeg timed out after %s: %w", ffmpegTimeout, err)
	}
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("ffmpeg cancelled: %w", ctx.Err())
	}
	return err
}

// convertToHLS は入力動画を 360p / 480p / 720p / 1080p の HLS（H.264 + AAC、10 秒セグメント）に変換し、
// 各画質のプレイリストを束ねるマスタープレイリスト hlsOutputDir/playlist.m3u8 を書き出す。
func convertToHLS(ctx context.Context, inputPath, hlsOutputDir, videoID string) error {
	// 画質ごとの高さとビットレート。
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

	// 1. 画質ごとに ffmpeg を実行して <高さ>p/ 配下にプレイリストとセグメントを作る
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

		// キーフレーム間隔を 48 フレームに固定し、シーン切り替えでの挿入を無効にして、
		// 画質間でセグメント境界を揃えやすくする。scale=-2:<高さ> で縦横比を保ったまま幅を偶数にする。
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

	// 2. マスタープレイリストを書き出す。BANDWIDTH は映像と音声のビットレートの合計、
	// RESOLUTION は 16:9 を仮定して高さから幅を計算した値。
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

// updateVideoStatus は動画の status と processing_details（画面に出す進捗文言）を更新する。失敗時はログも残す。
func updateVideoStatus(ctx context.Context, videoID, status, details string) error {
	_, err := db.Exec(ctx, "UPDATE videos SET status = $1, processing_details = $2, updated_at = NOW() WHERE id = $3", status, details, videoID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update video status for %s: %v", videoID, err)
	}
	return err
}

// deleteHandler は DELETE /api/videos/:id を処理する。投稿者本人または管理者だけが削除でき、
// 変換中のジョブを止めたうえで、DB の行、MinIO の HLS とサムネイルを削除する。
func deleteHandler(c *gin.Context) {
	// 1. 対象動画を取得し、削除権限を確認する
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

	// 2. 変換中なら止める（ジョブ側は行が消えたことを確認して、アップロード済みのオブジェクトを片付ける）。
	if cancelJob(videoID) {
		logger.Infof("[VideoID: %s] Cancelled in-progress conversion job for deletion", videoID)
	}

	// 3. 先に行を消し、変換ジョブが後から public に更新できないようにする。
	if _, err = db.Exec(context.Background(), "DELETE FROM videos WHERE id = $1", videoID); err != nil {
		logger.Errorf("Error deleting video record %s from DB: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete video record"})
		return
	}

	// 4. HLSはMinIOへ保存しているのでMinIOから削除する（失敗してもログのみで、削除自体は成功として返す）。
	if err := removeHLSMinIO(context.Background(), videoID); err != nil {
		logger.Errorf("Failed to delete HLS from MinIO for video %s: %v", videoID, err)
	}

	// 5. サムネイルを削除する。thumbnail_path（/videos/thumbnails/<id>）があればそこからキーを求め、無ければ既定のキーを使う。
	objectName := "thumbnails/" + videoID
	if thumbnailPath.Valid && thumbnailPath.String != "" {
		objectName = strings.TrimPrefix(thumbnailPath.String, "/videos/")
	}
	if err := videoMinioClient.RemoveObject(context.Background(), videoBucketName, objectName, minio.RemoveObjectOptions{}); err != nil {
		logger.Errorf("Failed to delete thumbnail from MinIO %s: %v", objectName, err)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Video deleted successfully"})
}

// deleteThumbnailHandler は DELETE /api/videos/:id/thumbnail を処理する。投稿者本人または管理者だけが、
// MinIO 上のサムネイルを削除して thumbnail_path を NULL にできる。
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

	// MinIO からの削除に失敗してもログのみで、DB の更新は続ける。
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
		// 1. 署名と有効期限を検証する。署名方式は HS256 に固定し、alg のすり替え（none など）を防ぐ。
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

		// 2. jti を必須とし、ログアウト済みトークンの一覧（Redis）に載っていないか確認する
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

		// 3. ユーザー ID（sub / userID / user_id のうち最初に見つかったもの）と管理者フラグをコンテキストへ渡す
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

// generateThumbnail は動画の 1 秒地点から 1 フレームを切り出し、幅 320 の JPEG サムネイルとして書き出す。
func generateThumbnail(ctx context.Context, videoPath, thumbnailPath string) error {
	return runFFmpeg(ctx, "-i", videoPath, "-ss", "00:00:01.000", "-vframes", "1", "-q:v", "2", "-vf", "scale=320:-1", thumbnailPath)
}

// uploadThumbnailToMinIO はローカルで生成したサムネイルを動画用 MinIO バケットの thumbnails/<videoID> に
// アップロードし、配信用 URL（/videos/thumbnails/<videoID>）を返す。失敗時は空文字を返す。
func uploadThumbnailToMinIO(ctx context.Context, videoID, localPath string) string {
	f, err := os.Open(localPath)
	if err != nil {
		logger.Errorf("[VideoID: %s] ERROR: Failed to open thumbnail for upload: %v", videoID, err)
		return ""
	}
	defer f.Close()

	// 先頭 512 バイトから Content-Type を判定する。
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
	// 再試行のたびに読み取り位置を先頭へ戻してから送る。
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

// contentTypeForHLS は HLS の各ファイルに対応する MIME タイプを返す。
// MinIO がブラウザ再生に必要な正しい Content-Type で配信できるようにするため。
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

// putObjectWithRetry は MinIO へのアップロードを、一時的な失敗に備えて最大 5 回（2・4・6・8 秒の待機を挟んで）試す。
// 想定している一時的な失敗は、video-upload-api の起動直後に video-storage の MinIO がまだ認証を
// 受け付けられない、という起動時の競合である。S3 API と認証情報自体は正しいため、数回の再試行で
// この競合は解消し、本当の設定ミスは最終的にエラーとして表に出る。ctx が終了したら途中で打ち切る。
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

// uploadHLSMinIO は localHlsDir 配下のすべてのファイルを動画用 MinIO バケットへアップロードする。
// オブジェクトキーは "<videoID>/<相対パス>"（バケット名の接頭辞なし）。これは nginx の配信ルートが
// "/videos/<videoID>/<path>" を bucket=videos として扱うことに合わせている。
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

// removeHLSMinIO は動画用 MinIO バケットから "<videoID>/" 配下のオブジェクトをすべて削除する。
// 一部で失敗しても残りの削除を続け、最初に起きたエラーを返す。
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
