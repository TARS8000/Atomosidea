// game-upload-api はゲーム（Unity WebGL ビルドの ZIP）の投稿・一覧・詳細・編集・削除を扱う HTTP サービス。
// アップロードされたファイルは sfsp-api に転送してスキャンさせ、展開・公開は game-worker が行う。
package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql" // <-- これを追加
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/queue"
	"github.com/atmosidea/shared/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

// サービス全体で共有する接続・設定値。main で環境変数から初期化する。
var (
	db             *pgxpool.Pool
	minioClient    *minio.Client
	jwtSecret      []byte
	bucketName     string
	sfspApiUrl     string
	sfspApiKey     string
	logger         *zap.SugaredLogger
	teamServiceURL string

	// アップロードサイズの上限（環境変数で上書き可能）
	maxGameUploadBytes  int64 = 1 << 30  // MAX_GAME_UPLOAD_BYTES（既定 1 GiB）
	maxImageUploadBytes int64 = 10 << 20 // MAX_IMAGE_UPLOAD_BYTES（既定 10 MiB）
)

// multipart のフィールドやヘッダーのための余裕分
const multipartOverheadBytes int64 = 1 << 20

// サムネイルとして受け付ける画像の Content-Type（http.DetectContentType の結果）
var allowedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// チームトークンは base36 の 24 文字。URL パスにそのまま埋め込むので形式を検証する。
var teamTokenPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,24}$`)

// teamPermission は team-service の GET /api/teams/<token>/permission の応答。
type teamPermission struct {
	CanView bool `json:"can_view"`
	CanPost bool `json:"can_post"`
}

// fetchTeamPermission は team-service に呼び出し元の権限を問い合わせる。
// authHeader は検証済みのトークンのときだけ渡す（空なら匿名として問い合わせる）。
// 通信エラー等は error を返すので、呼び出し側は拒否として扱う（fail closed）。
func fetchTeamPermission(ctx context.Context, teamToken, authHeader string) (*teamPermission, error) {
	if !teamTokenPattern.MatchString(teamToken) {
		return &teamPermission{}, nil
	}
	// TEAM_SERVICE_URL が未設定なら Docker Compose 内の既定の宛先を使う
	if teamServiceURL == "" {
		teamServiceURL = "http://team-service:8080"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", teamServiceURL+"/api/teams/"+teamToken+"/permission", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed for team %s: %w", teamToken, err)
	}
	defer resp.Body.Close()
	// チームが存在しない場合はエラーではなく「権限なし」として返す
	if resp.StatusCode == http.StatusNotFound {
		return &teamPermission{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("team service returned %d for team %s", resp.StatusCode, teamToken)
	}
	var body teamPermission
	// 応答は小さい JSON のはずなので、読み込みを 64 KiB までに制限する
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &body, nil
}

// teamAccessCheck は team-service の権限エンドポイントを呼び出し、呼び出し元が
// 指定チームに投稿できるかを確認する。投稿できる場合に true を返す。
// team_id が空の場合は全体公開の投稿として扱い、常に許可する。
// 問い合わせに失敗した場合は false（拒否）を返す。
// authMiddleware を通過したリクエストでのみ呼ぶ（Authorization はそのまま転送する）。
func teamAccessCheck(c *gin.Context, teamToken string) bool {
	if teamToken == "" {
		return true
	}
	perm, err := fetchTeamPermission(c.Request.Context(), teamToken, c.GetHeader("Authorization"))
	if err != nil {
		logger.Errorf("teamAccessCheck: %v", err)
		return false
	}
	return perm.CanPost
}

// Game は games テーブルの1行を API 応答用に表したもの。
// ThumbnailSfspJobID はサムネイルのスキャンジョブ ID、TeamID は限定公開先のチームトークン
// （全体公開なら空文字）。Scale・OffsetX・OffsetY・NativeWidth・NativeHeight は
// ゲーム画面の表示調整に使う値。
type Game struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Status             string     `json:"status"`
	GameURL            string     `json:"game_url"`
	ThumbnailURL       string     `json:"thumbnail_url"`
	ThumbnailSfspJobID *uuid.UUID `json:"thumbnail_sfsp_job_id,omitempty"`
	UploaderID         string     `json:"uploader_id"`
	TeamID             string     `json:"team_id"`
	Scale              float32    `json:"scale"`
	OffsetX            int        `json:"offset_x"`
	OffsetY            int        `json:"offset_y"`
	NativeWidth        int        `json:"native_width"`
	NativeHeight       int        `json:"native_height"`
	// ProcessingDetails は game-worker が書き込む進捗・失敗理由。投稿者（と管理者）にだけ返す。
	ProcessingDetails *string   `json:"processing_details,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// AdjustPayload は PUT /api/games/adjust/:id で受け取る表示調整値（拡大率とオフセット）。
type AdjustPayload struct {
	Scale   float32 `json:"scale"`
	OffsetX int     `json:"offset_x"`
	OffsetY int     `json:"offset_y"`
}

// SfspUploadResponse は sfsp-api の POST /api/v1/files の応答（ログと job_id の保存に使う分だけ）。
type SfspUploadResponse struct {
	FileID string `json:"file_id"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// VerificationResult はZIPファイルの検証結果とデバッグ情報を含みます
type VerificationResult struct {
	IsValid           bool   `json:"is_valid"`
	WasmFileFound     bool   `json:"wasm_file_found"`
	UnityKeywordFound bool   `json:"unity_keyword_found"`
	CheckedFile       string `json:"checked_file"`
}

// generateRandomID は crypto/rand で 5 バイトを生成し、10 文字の16進文字列のゲーム ID を返す。
// 既存 ID との重複は確認しない。
func generateRandomID() (string, error) {
	for {
		bytes := make([]byte, 5)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		id := hex.EncodeToString(bytes)
		// ID が数字だけにならず、必ず英字を1文字以上含む英数字になるようにする
		// （含まない場合は生成し直す）。
		if strings.ContainsAny(id, "abcdef") {
			return id, nil
		}
	}
}

// envInt64 は環境変数 name を正の int64 として読み取る。未設定なら def を返し、
// 数値でない・0 以下の場合は警告ログを出して def を返す。
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

// safeFilename はパス区切りを取り除き、送信先で一時ファイル名に使われても安全な名前にする。
func safeFilename(name, fallback string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return fallback
	}
	// 長すぎる名前は末尾 200 バイトだけ残す（拡張子を保つため後ろ側を使う）
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

// isBodyTooLarge は http.MaxBytesReader の上限超過によるエラーかを判定する。
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	// 型で判別できない形にラップされている場合に備え、エラーメッセージでも判定する
	return err != nil && strings.Contains(err.Error(), "request body too large")
}

// parseMultipartLimited はリクエストボディを limit バイトに制限して multipart を解析する。
// 8 MiB を超えるファイル部分はディスク上の一時ファイルに置かれる（メモリに丸ごと載せない）。
// 失敗時はレスポンスを書いて false を返す。
func parseMultipartLimited(c *gin.Context, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	if err := c.Request.ParseMultipartForm(8 << 20); err != nil {
		if isBodyTooLarge(err) {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request body too large"})
			return false
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart form"})
		return false
	}
	return true
}

// checkImageFile はサムネイルのサイズと形式（画像のみ）を確認し、先頭に巻き戻す。
// 問題があれば HTTP ステータスとメッセージを返す（0 なら OK）。
func checkImageFile(f multipart.File, header *multipart.FileHeader) (int, string) {
	if header.Size > maxImageUploadBytes {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("Thumbnail exceeds the maximum size of %d bytes", maxImageUploadBytes)
	}
	// http.DetectContentType が見るのは先頭 512 バイトまでなので、その分だけ読む
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return http.StatusBadRequest, "Failed to read thumbnail"
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return http.StatusInternalServerError, "Failed to process thumbnail"
	}
	if ct := http.DetectContentType(buf[:n]); !allowedImageTypes[ct] {
		return http.StatusUnsupportedMediaType, "Thumbnail must be a JPEG, PNG, GIF or WebP image"
	}
	return 0, ""
}

// submitToSFSP は src を multipart で sfsp-api にストリーミング送信する（io.Pipe を使い、
// 全体をメモリに溜めない）。すべての呼び出しに X-SFSP-API-Key を付ける。
func submitToSFSP(ctx context.Context, src io.Reader, filename, targetService string, timeout time.Duration) (*SfspUploadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 1. 別 goroutine で multipart 本文（target_service と file）をパイプに書き込む。
	//    失敗時は CloseWithError で読み込み側（HTTP 送信）にエラーを伝える。
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := func() error {
			if err := mw.WriteField("target_service", targetService); err != nil {
				return fmt.Errorf("failed to write target_service: %w", err)
			}
			part, err := mw.CreateFormFile("file", filename)
			if err != nil {
				return fmt.Errorf("failed to create form file: %w", err)
			}
			if _, err := io.Copy(part, src); err != nil {
				return fmt.Errorf("failed to stream file: %w", err)
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	// 書き込み側の goroutine が src を読み終える（または中断する）まで待ってから戻る。
	defer func() {
		pr.Close()
		<-done
	}()

	// 2. パイプの読み込み側を本文にして sfsp-api へ POST する
	req, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", pr)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFSP request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-SFSP-API-Key", sfspApiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SFSP request failed: %w", err)
	}
	defer resp.Body.Close()

	// 3. 応答を最大 1 MiB まで読み、2xx 以外はエラー、2xx なら JSON を解析して返す
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read SFSP response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("SFSP returned non-2xx status %d: %s", resp.StatusCode, string(respBytes))
	}
	var out SfspUploadResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, fmt.Errorf("failed to decode SFSP response: %w", err)
	}
	return &out, nil
}

// ensureGameSchema は既存DBへの後方互換性のため、gamesテーブルを作成し、
// team_idカラムが存在しなければ追加する。
// team_idがNULLのゲームが全体公開、チームトークンを保持するゲームがそのチーム限定公開になる。
func ensureGameSchema(ctx context.Context, pool *pgxpool.Pool) error {
	createGamesTableSQL := `
		CREATE TABLE IF NOT EXISTS public.games (
			id VARCHAR(10) PRIMARY KEY,
			user_id UUID NOT NULL,
			title VARCHAR(255) NOT NULL,
			description TEXT,
			status VARCHAR(50) NOT NULL DEFAULT 'processing',
			sfsp_job_id UUID,
			thumbnail_sfsp_job_id UUID,
			processing_details TEXT,
			-- team_idがNULLのゲームが全体公開、チームトークンを保持するゲームがそのチーム限定公開になる
			team_id VARCHAR(24),
			game_url VARCHAR(255),
			thumbnail_url VARCHAR(255),
			scale REAL DEFAULT 1.0 NOT NULL,
			offset_x INTEGER DEFAULT 0 NOT NULL,
			offset_y INTEGER DEFAULT 0 NOT NULL,
			native_width INTEGER DEFAULT 1280 NOT NULL,
			native_height INTEGER DEFAULT 720 NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
		)`

	_, err := pool.Exec(ctx, createGamesTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create games table: %w", err)
	}

	// 既存DBへの後方互換性のため、team_idカラムが存在しなければ追加する。
	_, err = pool.Exec(ctx, `ALTER TABLE public.games ADD COLUMN IF NOT EXISTS team_id VARCHAR(24)`)
	if err != nil {
		return fmt.Errorf("failed to add team_id column to games table: %w", err)
	}

	return nil
}

// main は環境変数から設定を読み込み、DB・MinIO・Redis に接続してから
// /api/games 配下のルートを登録し、ポート 8082 で HTTP サーバーを起動する。
func main() {
	// 1. ロガーを初期化する
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	// 2. 環境変数から設定を読み込む
	ctx := context.Background()
	databaseUrl := os.Getenv("DATABASE_URL")
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	minioAccessKey := os.Getenv("MINIO_ACCESS_KEY_ID")
	minioSecretKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	bucketName = os.Getenv("MINIO_BUCKET_NAME")
	sfspApiUrl = os.Getenv("SFSP_API_URL")
	sfspApiKey = os.Getenv("SFSP_API_KEY")
	teamServiceURL = os.Getenv("TEAM_SERVICE_URL")
	maxGameUploadBytes = envInt64("MAX_GAME_UPLOAD_BYTES", maxGameUploadBytes)
	maxImageUploadBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", maxImageUploadBytes)

	// 3. 必須の設定を確認する（SFSP_API_KEY は未設定でも起動はする）
	if sfspApiUrl == "" {
		logger.Fatalf("SFSP_API_URL environment variable is not set")
	}
	if sfspApiKey == "" {
		logger.Warn("SFSP_API_KEY is not set; requests to sfsp-api will be rejected")
	}
	if len(jwtSecret) == 0 {
		logger.Fatalf("JWT_SECRET environment variable is not set")
	}

	// 4. 共通設定を読み込み、DB に接続してスキーマを用意する
	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		logger.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	// 既存DBへの後方互換性のため、team_idカラムが存在しなければ追加する。
	// team_idがNULLのゲームが全体公開、チームトークンを保持するゲームがそのチーム限定公開になる。
	if err := ensureGameSchema(ctx, db); err != nil {
		logger.Fatalf("Failed to ensure game schema: %v", err)
	}

	// 5. MinIO クライアントを作成し、Redis（トークンの blocklist 確認に使う）に接続する
	minioClient, err = minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		logger.Fatalf("Unable to connect to MinIO: %v", err)
	}

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	// 6. ルートを登録してサーバーを起動する（一覧・詳細は認証任意、更新系は authMiddleware 必須）
	// 保存量・ディスク・処理待ちの判定と、分割アップロードの準備
	initUploadGuard(context.Background(), maxGameUploadBytes)

	// ユーザーごとのアップロード制限（UPLOAD_LIMIT_*、未設定なら既定値）
	uploadLimits := ratelimit.LoadUploadLimitsFromEnv()
	logger.Infof("Per-user upload limits: %d/hour, %d/day, %d concurrent", uploadLimits.PerHour, uploadLimits.PerDay, uploadLimits.Concurrent)
	r := gin.Default()
	// multipart の解析で 8 MiB を超える部分はディスクへ（parseMultipartLimited と合わせる）
	r.MaxMultipartMemory = 8 << 20
	api := r.Group("/api/games")
	{
		api.GET("", listGamesHandler)
		api.GET("/:id", gameDetailsHandler)
		api.POST("/upload", authMiddleware(), uploadLimitMiddleware(uploadLimits), uploadGameHandler)
		// 分割アップロード（Cloudflare の 100 MB 制限への対応）
		registerChunkRoutes(api, "/upload/chunks", authMiddleware())
		api.PUT("/:id", authMiddleware(), imageLimitMiddleware(), updateGameHandler)
		api.DELETE("/:id", authMiddleware(), deleteGameHandler)
		api.DELETE("/:id/thumbnail", authMiddleware(), deleteThumbnailHandler)
		api.PUT("/adjust/:id", authMiddleware(), adjustGameHandler)
	}

	logger.Infof("Game API service (read/write) starting on port 8082 (max game upload %d bytes, max image %d bytes)", maxGameUploadBytes, maxImageUploadBytes)
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（大きなアップロードを受けるので本文の読み取りは長めに許す）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8082",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

// uploadGameHandler は POST /api/games/upload を処理する。ゲーム ZIP（必須）と
// サムネイル（任意）を検証し、games に 'scanning' 状態の行を作ってから sfsp-api に
// スキャンを依頼し、202 とゲーム ID を返す。公開処理はスキャン完了後に game-worker が行う。
func uploadGameHandler(c *gin.Context) {
	// 認証済みユーザー ID を取得して UUID として検証する
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user session"})
		return
	}

	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		logger.Errorf("Invalid user ID format: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID format"})
		return
	}

	// リクエスト全体を「ゲーム + サムネイル + 余裕分」に制限する（超過は 413）
	if !parseMultipartLimited(c, maxGameUploadBytes+maxImageUploadBytes+multipartOverheadBytes) {
		return
	}

	// フォーム項目を取り出し、タイトル必須とチームへの投稿権限を確認する
	title := c.PostForm("title")
	description := c.PostForm("description")
	teamID := c.PostForm("team_id")
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}

	if !teamAccessCheck(c, teamID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to post to this team"})
		return
	}

	// ゲームファイルを取り出し、サイズ上限を確認する（分割アップロードなら upload_id から組み立て済みのファイルを開く）
	gameFile, gameOrigName, gameFileSize, ok := openMainUpload(c, "game")
	if !ok {
		return
	}
	defer gameFile.Close()

	if gameFileSize > maxGameUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("Game file exceeds the maximum size of %d bytes", maxGameUploadBytes)})
		return
	}
	// 保存量・ディスクの空き・処理待ちを確かめる
	if !guardUploadSize(c, gameFileSize) {
		return
	}
	gameFilename := safeFilename(gameOrigName, "game.zip")

	// ファイル形式チェック (ZIP)
	buffer := make([]byte, 512)
	n, err := io.ReadFull(gameFile, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if _, err := gameFile.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}
	contentType := http.DetectContentType(buffer[:n])
	isZipExtension := strings.HasSuffix(strings.ToLower(gameFilename), ".zip")

	// 内容判定と拡張子のどちらか一方が ZIP なら通す（実際に ZIP として読めるかは次の検証で確かめる）
	if contentType != "application/zip" && !isZipExtension {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error":                 "Unsupported file type. Only ZIP files are allowed.",
			"detected_content_type": contentType,
			"filename":              gameFilename,
			"is_zip_extension":      isZipExtension,
		})
		return
	}

	// Unity WebGLビルドのZIPファイルかどうかの検証
	verificationResult, err := verifyUnityWebGLZip(gameFile, gameFileSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to verify ZIP file: %v", err)})
		return
	}
	if !verificationResult.IsValid {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error":      "The uploaded ZIP file is not a valid Unity WebGL build.",
			"debug_info": verificationResult,
		})
		return
	}
	if _, err := gameFile.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader after verification"})
		return
	}

	// サムネイル（任意）: サイズと形式を SFSP に送る前に検証する
	thumbnailFile, thumbnailHeader, errThumb := c.Request.FormFile("thumbnail")
	hasThumbnail := false
	if errThumb == nil {
		defer thumbnailFile.Close()
		if status, msg := checkImageFile(thumbnailFile, thumbnailHeader); status != 0 {
			c.JSON(status, gin.H{"error": msg})
			return
		}
		hasThumbnail = true
	} else if errThumb != http.ErrMissingFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thumbnail file"})
		return
	}

	// ゲーム ID を採番する
	gameID, err := generateRandomID()
	if err != nil {
		logger.Errorf("Error generating random ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate game ID"})
		return
	}

	// team_id が空なら NULL（全体公開）として保存する
	teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}

	// スキャン完了イベントが INSERT より先に届いて捨てられないよう、
	// SFSP に送る前に行を作っておき、ジョブ ID は応答を受け取った直後に UPDATE する。
	// （UPDATE までの短い隙間は game-worker 側の再試行で吸収する）
	_, err = db.Exec(
		context.Background(),
		`INSERT INTO games
        (id, user_id, title, description, status, team_id, upload_bytes)
        VALUES ($1, $2, $3, $4, 'scanning', $5, $6)`,
		gameID,
		userUUID,
		title,
		description,
		teamIDVal,
		gameFileSize,
	)
	if err != nil {
		logger.Errorf("Error creating initial game record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create game record"})
		return
	}

	// 途中で失敗したら作成した行を削除する（後から届くイベントは worker が「行なし」として破棄する）
	abort := func(status int, msg string) {
		if _, err := db.Exec(context.Background(), "DELETE FROM games WHERE id = $1", gameID); err != nil {
			logger.Errorf("[GameID: %s] Failed to delete game record after upload error: %v", gameID, err)
		}
		c.JSON(status, gin.H{"error": msg})
	}

	// ゲーム ZIP を sfsp-api に送ってスキャンを依頼する（タイムアウト 10 分）
	sfspRespData, err := submitToSFSP(c.Request.Context(), gameFile, gameFilename, "game", 10*time.Minute)
	if err != nil {
		logger.Errorf("[GameID: %s] Error forwarding file to SFSP: %v", gameID, err)
		abort(http.StatusServiceUnavailable, "File scanning service is unavailable")
		return
	}

	logger.Infof("[GameID: %s] SFSP response: file_id=%s job_id=%s status=%s", gameID, sfspRespData.FileID, sfspRespData.JobID, sfspRespData.Status)

	sfspJobID, err := uuid.Parse(sfspRespData.JobID)
	if err != nil {
		logger.Errorf("[GameID: %s] Invalid Job ID from SFSP. file_id=%s job_id=%q status=%s", gameID, sfspRespData.FileID, sfspRespData.JobID, sfspRespData.Status)
		abort(http.StatusInternalServerError, "Invalid Job ID from SFSP")
		return
	}

	// sfsp-api は常に "queued" を返し、スキャン結果は完了イベントで届く（status は INSERT 時の 'scanning' のまま）
	if _, err := db.Exec(context.Background(),
		"UPDATE games SET sfsp_job_id = $1, updated_at = NOW() WHERE id = $2",
		sfspJobID, gameID); err != nil {
		logger.Errorf("[GameID: %s] Error saving SFSP job ID: %v", gameID, err)
		abort(http.StatusInternalServerError, "Failed to create game record")
		return
	}

	// サムネイルがあれば同様に sfsp-api へ送り、そのジョブ ID を保存する
	if hasThumbnail {
		thumbFilename := safeFilename(thumbnailHeader.Filename, "thumbnail")
		thumbSfspResp, err := submitToSFSP(c.Request.Context(), thumbnailFile, thumbFilename, "game-thumbnail", 2*time.Minute)
		if err != nil {
			logger.Errorf("[GameID: %s] Error forwarding thumbnail to SFSP: %v", gameID, err)
			abort(http.StatusServiceUnavailable, "Thumbnail scanning service is unavailable")
			return
		}
		thumbJobID, err := uuid.Parse(thumbSfspResp.JobID)
		if err != nil {
			logger.Errorf("[GameID: %s] Invalid Thumbnail Job ID from SFSP: %v", gameID, err)
			abort(http.StatusInternalServerError, "Invalid thumbnail job ID from SFSP")
			return
		}
		if _, err := db.Exec(context.Background(),
			"UPDATE games SET thumbnail_sfsp_job_id = $1 WHERE id = $2", thumbJobID, gameID); err != nil {
			logger.Errorf("[GameID: %s] Error saving thumbnail SFSP job ID: %v", gameID, err)
			abort(http.StatusInternalServerError, "Failed to create game record")
			return
		}
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Game upload accepted",
		"gameId":  gameID,
		"status":  "scanning",
	})
}

// wasm 内のキーワード探索で読む最大バイト数（展開後）。zip bomb / gzip bomb / brotli bomb 対策。
// 圧縮の有無に関わらず、展開後のバイト列をこの上限までしか読まない。
const maxWasmScanBytes int64 = 512 << 20

// Unity の wasm に含まれるキーワード
var unityWasmKeywords = [][]byte{[]byte("il2cpp"), []byte("UnityEngine")}

// verifyUnityWebGLZip は ZIP 内の .wasm / .wasm.br / .wasm.gz を順に調べ、Unity のキーワードを
// 含むものが見つかれば有効と判定する。CheckedFile には最後に調べた wasm のファイル名が入る。
// ZIP として読めない場合や wasm の展開に失敗した場合は error を返す。
func verifyUnityWebGLZip(file multipart.File, size int64) (*VerificationResult, error) {
	result := &VerificationResult{}

	zipReader, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("failed to read zip file: %w", err)
	}

	for _, f := range zipReader.File {
		// 拡張子から圧縮形式を決める（wasm 以外のファイルは飛ばす）
		var compression string
		switch {
		case strings.HasSuffix(f.Name, ".wasm"):
		case strings.HasSuffix(f.Name, ".wasm.br"):
			compression = "br"
		case strings.HasSuffix(f.Name, ".wasm.gz"):
			compression = "gzip"
		default:
			continue
		}
		result.WasmFileFound = true
		result.CheckedFile = f.Name

		found, err := scanWasmForUnity(f, compression)
		if err != nil {
			return nil, err
		}
		if found {
			result.UnityKeywordFound = true
			break
		}
	}

	result.IsValid = result.WasmFileFound && result.UnityKeywordFound
	return result, nil
}

// scanWasmForUnity は zip 内の wasm（compression: "" / "br" / "gzip"）を展開しながら
// Unity のキーワードを探す。
func scanWasmForUnity(f *zip.File, compression string) (bool, error) {
	rc, err := f.Open()
	if err != nil {
		return false, fmt.Errorf("failed to open wasm file in zip: %w", err)
	}
	defer rc.Close()

	var reader io.Reader = rc
	switch compression {
	case "br":
		reader = brotli.NewReader(rc)
	case "gzip":
		gz, err := gzip.NewReader(rc)
		if err != nil {
			return false, fmt.Errorf("failed to read gzip wasm file %s: %w", f.Name, err)
		}
		defer gz.Close()
		reader = gz
	}
	return containsUnityKeyword(io.LimitReader(reader, maxWasmScanBytes))
}

// containsUnityKeyword は r を固定サイズのチャンクで読み、unityWasmKeywords のいずれかを含むかを返す。
// チャンクの境界をまたぐキーワードを見落とさないよう、前のチャンクの末尾を重ねて検索する。
// （wasm はバイナリなので、改行区切りの bufio.Scanner だと長い行で ErrTooLong になりうる）
func containsUnityKeyword(r io.Reader) (bool, error) {
	// 重ねる長さは「最長キーワードの長さ - 1」
	overlap := 0
	for _, kw := range unityWasmKeywords {
		if len(kw)-1 > overlap {
			overlap = len(kw) - 1
		}
	}
	const chunkSize = 64 << 10
	buf := make([]byte, overlap+chunkSize)
	carry := 0
	for {
		// 前回の末尾（carry バイト）の後ろに続きを読み込み、全体を検索窓にする
		n, err := io.ReadFull(r, buf[carry:])
		window := buf[:carry+n]
		for _, kw := range unityWasmKeywords {
			if bytes.Contains(window, kw) {
				return true, nil
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("error scanning wasm file: %w", err)
		}
		// 窓の末尾 overlap バイトをバッファ先頭へ移し、次の読み込みと重ねて検索する
		carry = copy(buf, window[len(window)-overlap:])
	}
}

// listGamesHandler は GET /api/games を処理する。公開中（status = 'public'）のゲームを
// 新しい順に最大 50 件返す。q でタイトルの部分一致検索（大文字小文字を区別しない）、
// team でチーム限定ゲームの一覧（閲覧権限が必要）に切り替える。
func listGamesHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	teamToken := c.Query("team")

	// team パラメータ: ある場合はそのチーム限定(team_id = $)、なければ公開(team_id IS NULL)のみ。
	// status='public' のみを表示。プレースホルダーは args の順序に合わせて番号を振る。
	whereConds := []string{"status = 'public'"}
	var args []interface{}
	argIdx := 1
	if teamToken != "" {
		// チーム限定の一覧は閲覧権限（can_view）がある場合だけ返す
		// 検証に通ったトークンだけを team-service に転送する（無効なら匿名として問い合わせる）
		authHeader := ""
		if optionalAuth(c) != nil {
			authHeader = c.GetHeader("Authorization")
		}
		perm, err := fetchTeamPermission(c.Request.Context(), teamToken, authHeader)
		if err != nil {
			logger.Errorf("listGamesHandler: team permission check failed: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to verify team permission"})
			return
		}
		if !perm.CanView {
			c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to view this team's games"})
			return
		}
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

	// 条件はプレースホルダーで渡すので、検索語や team の値が SQL に直接埋め込まれることはない
	var rows pgx.Rows
	var err error
	rows, err = db.Query(context.Background(),
		`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), user_id, scale, offset_x, offset_y, native_width, native_height, created_at
         FROM games
         `+whereClause+`
         ORDER BY created_at DESC LIMIT 50`, args...)

	if err != nil {
		logger.Errorf("ERROR: Database query failed in listGamesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing games"})
		return
	}
	defer rows.Close()

	// 空配列で初期化し、該当なしでも null ではなく [] を返す。読み取りに失敗した行は飛ばす。
	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.GameURL, &g.ThumbnailURL, &g.UploaderID, &g.Scale, &g.OffsetX, &g.OffsetY, &g.NativeWidth, &g.NativeHeight, &g.CreatedAt); err != nil {
			logger.Errorf("Error scanning game row: %v", err)
			continue
		}
		// team_id は SELECT していないが、条件から全行が teamToken（全体公開なら空）と一致する
		g.TeamID = teamToken
		games = append(games, g)
	}
	c.JSON(http.StatusOK, games)
}

// gameDetailsHandler は GET /api/games/:id を処理し、ゲーム1件の詳細を返す。
// gameDetailsHandler の公開範囲:
//   - 投稿者本人（と管理者）は status に関係なく自分のゲームを見られる
//   - それ以外は status = 'public' のものだけ（それ以外は 404）
//   - team_id があるゲームは、team-service が can_view を返した場合だけ（それ以外は 403）
func gameDetailsHandler(c *gin.Context) {
	gameID := c.Param("id")
	var g Game
	err := db.QueryRow(context.Background(),
		`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), thumbnail_sfsp_job_id, user_id, COALESCE(team_id, ''), scale, offset_x, offset_y, native_width, native_height, processing_details, created_at
        FROM games
        WHERE id = $1`, gameID).Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.GameURL, &g.ThumbnailURL, &g.ThumbnailSfspJobID, &g.UploaderID, &g.TeamID, &g.Scale, &g.OffsetX, &g.OffsetY, &g.NativeWidth, &g.NativeHeight, &g.ProcessingDetails, &g.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		} else {
			logger.Errorf("ERROR: Database query failed in gameDetailsHandler: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve game"})
		}
		return
	}

	// 投稿者本人か管理者なら、内部情報も含めてそのまま返す
	viewer := optionalAuth(c)
	if viewer != nil && (viewer.UserID == g.UploaderID || viewer.IsAdmin) {
		c.JSON(http.StatusOK, g)
		return
	}

	// 非公開のゲームは存在自体を明かさないよう 404 にする
	if g.Status != "public" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}
	// チーム限定のゲームは team-service に閲覧権限を確認する
	if g.TeamID != "" {
		authHeader := ""
		if viewer != nil {
			authHeader = c.GetHeader("Authorization")
		}
		perm, err := fetchTeamPermission(c.Request.Context(), g.TeamID, authHeader)
		if err != nil {
			logger.Errorf("gameDetailsHandler: team permission check failed: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to verify team permission"})
			return
		}
		if !perm.CanView {
			c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to view this game"})
			return
		}
	}
	// 投稿者以外にはスキャンジョブの ID や処理の詳細（内部情報）を見せない
	g.ThumbnailSfspJobID = nil
	g.ProcessingDetails = nil
	c.JSON(http.StatusOK, g)
}

// updateGameHandler は PUT /api/games/:id を処理する。投稿者本人だけがタイトルと説明を
// 更新でき、サムネイルが添付されていれば sfsp-api にスキャンを依頼して 202 を返す
// （サムネイルの差し替え自体はスキャン完了後に game-worker が行う）。
func updateGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

	// 投稿者本人かを確認する（管理者でも他人のゲームは編集できない）
	userID := c.GetString("userID")
	var uploaderID string
	err := db.QueryRow(context.Background(), "SELECT user_id FROM games WHERE id = $1", gameID).Scan(&uploaderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}
	if userID != uploaderID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to edit this game"})
		return
	}

	// サムネイル + テキスト項目のみなので画像の上限 + 余裕分に制限する
	if !parseMultipartLimited(c, maxImageUploadBytes+multipartOverheadBytes) {
		return
	}

	// サムネイルは DB を更新する前に検証する（不正な画像でタイトルだけ変わるのを防ぐ）
	thumbnailFile, thumbnailHeader, errThumb := c.Request.FormFile("thumbnail")
	if errThumb != nil && errThumb != http.ErrMissingFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thumbnail file"})
		return
	}
	if errThumb == nil {
		defer thumbnailFile.Close()
		if status, msg := checkImageFile(thumbnailFile, thumbnailHeader); status != 0 {
			c.JSON(status, gin.H{"error": msg})
			return
		}
	}

	// タイトルと説明を更新する
	title := c.PostForm("title")
	description := c.PostForm("description")

	_, err = db.Exec(context.Background(),
		"UPDATE games SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
		title, description, gameID)
	if err != nil {
		logger.Errorf("Error updating game details: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update game details"})
		return
	}

	// サムネイルが無ければここで完了
	if errThumb == http.ErrMissingFile {
		c.JSON(http.StatusOK, gin.H{"message": "Game details updated successfully"})
		return
	}

	// サムネイルを sfsp-api に送り、そのジョブ ID を保存する
	thumbFilename := safeFilename(thumbnailHeader.Filename, "thumbnail")
	thumbSfspResp, err := submitToSFSP(c.Request.Context(), thumbnailFile, thumbFilename, "game-thumbnail", 2*time.Minute)
	if err != nil {
		logger.Errorf("Error forwarding thumbnail to SFSP: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Thumbnail scanning service is unavailable"})
		return
	}

	parsedThumbJobID, err := uuid.Parse(thumbSfspResp.JobID)
	if err != nil {
		logger.Errorf("Invalid Thumbnail Job ID from SFSP: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid thumbnail job ID from SFSP"})
		return
	}

	_, err = db.Exec(context.Background(),
		"UPDATE games SET thumbnail_sfsp_job_id = $1 WHERE id = $2",
		parsedThumbJobID, gameID)
	if err != nil {
		logger.Errorf("Error updating game thumbnail SFSP job ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update game thumbnail"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Thumbnail upload accepted for scanning",
		"job_id":  parsedThumbJobID.String(),
		"status":  "scanning",
	})
}

// adjustGameHandler は PUT /api/games/adjust/:id を処理し、投稿者本人が指定した
// 表示調整値（scale・offset_x・offset_y）を保存する。値の範囲は検証しない。
func adjustGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

	// 投稿者本人かを確認する
	userID := c.GetString("userID")
	var uploaderID string
	err := db.QueryRow(context.Background(), "SELECT user_id FROM games WHERE id = $1", gameID).Scan(&uploaderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}
	if userID != uploaderID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to adjust this game"})
		return
	}

	var payload AdjustPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	_, err = db.Exec(context.Background(),
		"UPDATE games SET scale = $1, offset_x = $2, offset_y = $3, updated_at = NOW() WHERE id = $4",
		payload.Scale, payload.OffsetX, payload.OffsetY, gameID)
	if err != nil {
		logger.Errorf("Error updating game adjustments: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save adjustments"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Adjustments saved successfully"})
}

// deleteGameHandler は DELETE /api/games/:id を処理する。投稿者本人または管理者が、
// MinIO 上のゲームファイルとサムネイルを削除した後に DB の行を削除する。
func deleteGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

	// 投稿者本人または管理者かを確認する
	userID := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	var uploaderID string
	err := db.QueryRow(context.Background(), "SELECT user_id FROM games WHERE id = $1", gameID).Scan(&uploaderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}

	if userID != uploaderID && !isAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this game"})
		return
	}

	ctx := context.Background()
	objectPrefix := fmt.Sprintf("%s/", gameID)

	// バケット内の <gameID>/ 配下のオブジェクトを列挙してチャネルに流し、RemoveObjects でまとめて削除する。
	// 列挙でエラーが出たらそこで列挙を打ち切る（それまでに流した分だけが削除される）。
	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		for object := range minioClient.ListObjects(ctx, bucketName, minio.ListObjectsOptions{
			Prefix:    objectPrefix,
			Recursive: true,
		}) {
			if object.Err != nil {
				logger.Errorf("Error listing object for deletion: %v", object.Err)
				return
			}
			objectsCh <- object
		}
	}()

	errorCh := minioClient.RemoveObjects(ctx, bucketName, objectsCh, minio.RemoveObjectsOptions{})
	for err := range errorCh {
		logger.Errorf("Error deleting object %s for game %s: %v", err.ObjectName, gameID, err.Err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete game files from storage"})
		return
	}

	// サムネイルはゲーム本体（バケット内の <gameID>/ 配下）ではなく、別のオブジェクト
	// thumbnails/<gameID> に保存されているので、孤立したファイルが残らないよう明示的に削除する。
	thumbObjectName := fmt.Sprintf("thumbnails/%s", gameID)
	if err := minioClient.RemoveObject(ctx, bucketName, thumbObjectName, minio.RemoveObjectOptions{}); err != nil {
		logger.Errorf("Error deleting thumbnail %s for game %s: %v", thumbObjectName, gameID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete game thumbnail from storage"})
		return
	}

	// ストレージの削除が終わってから DB の行を削除する
	_, err = db.Exec(ctx, "DELETE FROM games WHERE id = $1", gameID)
	if err != nil {
		logger.Errorf("Error deleting game record %s from DB: %v", gameID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete game record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Game deleted successfully"})
}

// deleteThumbnailHandler は DELETE /api/games/:id/thumbnail を処理する。投稿者本人だけが、
// MinIO 上のサムネイルを削除して thumbnail_url を NULL に戻せる。
func deleteThumbnailHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

	// 投稿者本人かを確認し、現在のサムネイル URL を取得する
	userID := c.GetString("userID")
	var uploaderID string
	var thumbnailURL sql.NullString
	err := db.QueryRow(context.Background(), "SELECT user_id, thumbnail_url FROM games WHERE id = $1", gameID).Scan(&uploaderID, &thumbnailURL)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		} else {
			logger.Errorf("ERROR: Database query failed in deleteThumbnailHandler: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve game"})
		}
		return
	}
	if userID != uploaderID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this thumbnail"})
		return
	}

	// thumbnail_url（"/games/thumbnails/<gameID>" の形式）から先頭の "/games/" を除いて MinIO のオブジェクト名にする
	if thumbnailURL.Valid && thumbnailURL.String != "" {
		objectName := strings.TrimPrefix(thumbnailURL.String, "/games/")
		err := minioClient.RemoveObject(context.Background(), bucketName, objectName, minio.RemoveObjectOptions{})
		if err != nil {
			logger.Errorf("Failed to delete thumbnail from MinIO: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete thumbnail from storage"})
			return
		}
	}

	_, err = db.Exec(context.Background(), "UPDATE games SET thumbnail_url = NULL WHERE id = $1", gameID)
	if err != nil {
		logger.Errorf("Failed to update thumbnail_url in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update game record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Thumbnail deleted successfully"})
}

// authInfo は検証済み JWT から取り出した呼び出し元の情報。
type authInfo struct {
	UserID  string
	IsAdmin bool
}

// validateToken は Authorization ヘッダーの JWT を検証する（HS256 固定）。
// 署名検証の後に Redis の blocklist:<jti> を確認し、ログアウト済みのトークンを拒否する。
// Redis のエラー時は 500 で拒否する（fail closed。auth-service と同じ挙動）。
// 失敗時は HTTP ステータスとメッセージを返す。
func validateToken(ctx context.Context, authHeader string) (*authInfo, int, string) {
	if authHeader == "" {
		return nil, http.StatusUnauthorized, "Authorization header is missing"
	}
	// 1. 署名を検証する（アルゴリズムを HS256 に固定し、alg 差し替え攻撃を防ぐ）
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return nil, http.StatusUnauthorized, "Invalid token"
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, http.StatusUnauthorized, "Invalid token"
	}

	// 2. jti が blocklist に載っていないか（ログアウト済みでないか）を Redis で確認する
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return nil, http.StatusUnauthorized, "Invalid token"
	}
	exists, err := queue.RedisClient.Exists(ctx, "blocklist:"+jti).Result()
	if err != nil {
		logger.Errorf("Redis error checking token blocklist: %v", err)
		return nil, http.StatusInternalServerError, "Failed to verify token"
	}
	if exists > 0 {
		return nil, http.StatusUnauthorized, "Token has been invalidated"
	}

	// 3. ユーザー ID を sub・userID・user_id・id の順に探して取り出し、UUID 形式であることを確認する
	var userID string
	for _, key := range []string{"sub", "userID", "user_id", "id"} {
		if val, ok := claims[key].(string); ok && val != "" {
			userID = val
			break
		}
	}
	if userID == "" {
		return nil, http.StatusUnauthorized, "Invalid user ID in token"
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, http.StatusUnauthorized, "Invalid user ID in token"
	}

	isAdmin, _ := claims["isAdmin"].(bool)
	return &authInfo{UserID: userID, IsAdmin: isAdmin}, 0, ""
}

// optionalAuth は認証が任意のエンドポイント用。トークンが無い・無効・失効済み・
// 確認できない（Redis エラー）場合はいずれも匿名（nil）として扱う。
func optionalAuth(c *gin.Context) *authInfo {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return nil
	}
	info, _, _ := validateToken(c.Request.Context(), authHeader)
	return info
}

// authMiddleware は認証必須のルート用ミドルウェア。validateToken に失敗したら
// そのステータスで中断し、成功したら userID と isAdmin を gin.Context に設定する。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		info, status, msg := validateToken(c.Request.Context(), c.GetHeader("Authorization"))
		if info == nil {
			c.AbortWithStatusJSON(status, gin.H{"error": msg})
			return
		}
		c.Set("userID", info.UserID)
		c.Set("isAdmin", info.IsAdmin)
		c.Next()
	}
}
