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
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/queue"
	"github.com/atmosidea/shared/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

// パッケージ全体で共有する接続・設定値。main で初期化し、各ハンドラから参照する。
var (
	db             *sql.DB
	minioClient    *minio.Client
	jwtSecret      string
	minioBucket    string
	sfspApiUrl     string
	sfspApiKey     string
	logger         *zap.SugaredLogger
	teamServiceURL string

	// アップロードサイズの上限（環境変数で上書き可能）
	maxSiteUploadBytes  int64 = 524288000 // MAX_SITE_UPLOAD_BYTES
	maxImageUploadBytes int64 = 10485760  // MAX_IMAGE_UPLOAD_BYTES

	// sfsp-api への送信は大きな ZIP をストリーミングするため長め、team-service への権限確認は短めのタイムアウトにする
	sfspHTTPClient = &http.Client{Timeout: 10 * time.Minute}
	teamHTTPClient = &http.Client{Timeout: 10 * time.Second}
)

// multipart のフィールド・ヘッダー分の余裕
const multipartOverheadBytes = 1 << 20

// multipart を解析するときにメモリに置く上限（超えた分は一時ファイルに書かれる）
const multipartMemoryBytes = 8 << 20

// allowedThumbnailTypes はサムネイルとして受け付ける画像の Content-Type。
// static-site-worker の imageExtensions と揃える。
var allowedThumbnailTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// チームトークンは base36 の 24 文字。URL パスにそのまま埋め込むので形式を検証する（game-upload-api と同じ）。
var teamTokenPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,24}$`)

// teamPermission は team-service の GET /api/teams/<token>/permission の応答。
type teamPermission struct {
	CanView bool `json:"can_view"`
	CanPost bool `json:"can_post"`
}

// fetchTeamPermission は team-service に呼び出し元の権限を問い合わせる。
// 形式が不正なトークンは問い合わせずに権限なしとする。
// authHeader は検証済みのトークンのときだけ渡す（空なら匿名として問い合わせる）。
// 通信エラー等は error を返すので、呼び出し側は拒否として扱う（fail closed）。
func fetchTeamPermission(ctx context.Context, teamToken, authHeader string) (*teamPermission, error) {
	// トークン形式を検証し、パス操作などの不正な値を URL に埋め込まないようにする
	if !teamTokenPattern.MatchString(teamToken) {
		return &teamPermission{}, nil
	}
	// TEAM_SERVICE_URL 未設定時は Docker Compose 内のサービス名を既定値として使う
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
	resp, err := teamHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed for team %s: %w", teamToken, err)
	}
	defer resp.Body.Close()
	// チームが存在しない（404）場合はエラーではなく権限なしとして扱う
	if resp.StatusCode == http.StatusNotFound {
		return &teamPermission{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("team service returned %d for team %s", resp.StatusCode, teamToken)
	}
	// 応答本文は 64KiB までに制限して読み込む
	var perm teamPermission
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&perm); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &perm, nil
}

// viewerAuthHeader は optionalUser で検証できた場合だけ Authorization ヘッダーを返す
// （無効なトークンを team-service に転送しない）。
func viewerAuthHeader(c *gin.Context, userID string) string {
	if userID == "" {
		return ""
	}
	return c.GetHeader("Authorization")
}

// teamAccessCheck は team-service の権限エンドポイントに問い合わせ、呼び出し元が
// 指定チームへ投稿できるかを確認する。投稿可能なら true を返す。
// team_id が空の場合は公開投稿として扱い、常に許可する。
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

// StaticSite は static_sites テーブルの1行（静的サイト1件）を表し、API の応答にも使う。
type StaticSite struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// 公開投稿なら空文字
	TeamID         string  `json:"team_id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	MinioPath      string  `json:"minio_path"`
	EntryPointPath string  `json:"entry_point_path"`
	ThumbnailURL   *string `json:"thumbnail_url"`
	// アップロード時は 'scanning' で作成される。一覧には 'public' のものだけを出す
	Status string `json:"status"`
	// 投稿者・管理者以外には返さない内部情報
	ProcessingDetails *string   `json:"processing_details,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	// 投稿者・管理者以外には返さない内部情報
	ThumbnailSfspJobID *uuid.UUID `json:"thumbnail_sfsp_job_id,omitempty"`
}

// SfspUploadResponse は sfsp-api の POST /api/v1/files の応答（使うのは job_id だけ）。
// スキャン結果は常に完了イベントで届く（sfsp-api は status "queued" しか返さない）。
type SfspUploadResponse struct {
	JobID string `json:"job_id"`
}

// envInt64 は環境変数 name を正の int64 として読み込む。
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

// main は各種設定と外部接続（PostgreSQL・MinIO・Redis）を初期化し、HTTP サーバーを起動する。
func main() {
	// 1. ロガーを初期化する
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	// 2. アップロード上限と team-service の URL を環境変数から読み込む
	maxSiteUploadBytes = envInt64("MAX_SITE_UPLOAD_BYTES", maxSiteUploadBytes)
	maxImageUploadBytes = envInt64("MAX_IMAGE_UPLOAD_BYTES", maxImageUploadBytes)
	if v := os.Getenv("TEAM_SERVICE_URL"); v != "" {
		teamServiceURL = strings.TrimRight(v, "/")
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// 3. PostgreSQL に接続する（起動順の都合で DB が未準備のことがあるため、5 秒間隔で最大 5 回試す）
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
	logger.Info("Successfully connected to PostgreSQL!")

	// 4. MinIO に接続する（ListBuckets で疎通を確認し、DB と同様に最大 5 回試す）
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

	// バケットの作成と匿名ダウンロードの設定は minio-init が行う

	// 5. Redis に接続する（JWT のブロックリスト照合に使う）
	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

	// 6. JWT 署名鍵と sfsp-api（ファイルスキャン）の接続情報を読み込む
	jwtSecret = os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		logger.Fatal("JWT_SECRET environment variable not set")
	}

	sfspApiUrl = os.Getenv("SFSP_API_URL")
	if sfspApiUrl == "" {
		logger.Fatal("SFSP_API_URL environment variable not set")
	}
	sfspApiKey = os.Getenv("SFSP_API_KEY")
	if sfspApiKey == "" {
		logger.Warn("SFSP_API_KEY is not set; requests to sfsp-api will be rejected")
	}

	// 7. ルーティングを設定する（閲覧系は認証不要、作成・更新・削除は authMiddleware を通す）
	// ユーザーごとのアップロード制限（UPLOAD_LIMIT_*、未設定なら既定値）
	uploadLimits := ratelimit.LoadUploadLimitsFromEnv()
	logger.Infof("Per-user upload limits: %d/hour, %d/day, %d concurrent", uploadLimits.PerHour, uploadLimits.PerDay, uploadLimits.Concurrent)
	r := gin.Default()
	r.MaxMultipartMemory = multipartMemoryBytes

	r.GET("/api/static-sites", listStaticSitesHandler)
	r.GET("/api/static-sites/:id", getStaticSiteHandler)

	auth := r.Group("/")
	auth.Use(authMiddleware)
	{
		auth.POST("/api/static-sites/upload", uploadLimitMiddleware(uploadLimits), uploadStaticSiteHandler)
		auth.PUT("/api/static-sites/:id", updateStaticSiteHandler)
		auth.DELETE("/api/static-sites/:id", deleteStaticSiteHandler)
		auth.DELETE("/api/static-sites/:id/thumbnail", deleteThumbnailHandler)
	}

	// 8. サーバーを起動する（既定ポートは 8085）
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	logger.Infof("Static Site Upload API running on port %s", port)
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（大きなアップロードを受けるので本文の読み取りは長めに許す）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	logger.Fatal(srv.ListenAndServe())
}

// authenticate は Authorization ヘッダーの JWT を検証し、ブロックリスト（blocklist:<jti>）を確認する。
// status が 0 なら成功。Redis のエラー時は拒否する（Fail-Closed）。
func authenticate(c *gin.Context) (userID string, isAdmin bool, status int, msg string) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false, http.StatusUnauthorized, "Authorization header required"
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")

	// 署名アルゴリズムを HS256 に限定し、alg すり替え攻撃を防ぐ
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return "", false, http.StatusUnauthorized, "Invalid token"
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", false, http.StatusUnauthorized, "Invalid token claims"
	}

	// logoutHandler（auth-service）は "blocklist:<jti>" で登録するため、同じキーで照合する
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return "", false, http.StatusUnauthorized, "Invalid token"
	}
	exists, err := queue.RedisClient.Exists(c.Request.Context(), "blocklist:"+jti).Result()
	if err != nil {
		logger.Errorf("Redis error checking blocklist: %v", err)
		return "", false, http.StatusInternalServerError, "Failed to verify token"
	}
	if exists > 0 {
		return "", false, http.StatusUnauthorized, "Token has been invalidated"
	}

	// ユーザー ID は発行元によってクレーム名が異なりうるため、候補を順に探して最初に見つかったものを使う
	for _, key := range []string{"sub", "userID", "user_id", "id"} {
		if val, ok := claims[key].(string); ok && val != "" {
			userID = val
			break
		}
	}
	if userID == "" {
		return "", false, http.StatusUnauthorized, "Invalid user ID in token"
	}
	if _, err := uuid.Parse(userID); err != nil {
		return "", false, http.StatusUnauthorized, "Invalid user ID in token"
	}
	isAdmin, _ = claims["isAdmin"].(bool)
	return userID, isAdmin, 0, ""
}

// authMiddleware は JWT でリクエストを認証する。失敗時は中断してエラーを返し、
// 成功時はコンテキストに userID と isAdmin を設定して次へ進む。
func authMiddleware(c *gin.Context) {
	userID, isAdmin, status, msg := authenticate(c)
	if status != 0 {
		c.AbortWithStatusJSON(status, gin.H{"error": msg})
		return
	}
	c.Set("userID", userID)
	c.Set("isAdmin", isAdmin)
	c.Next()
}

// optionalUser は公開 API 用。トークンが無い・無効・失効済み・検証できない場合は匿名として扱う。
func optionalUser(c *gin.Context) (string, bool) {
	if c.GetHeader("Authorization") == "" {
		return "", false
	}
	userID, isAdmin, status, _ := authenticate(c)
	if status != 0 {
		return "", false
	}
	return userID, isAdmin
}

// generateRandomID は暗号論的乱数 5 バイトから 16 進 10 文字のサイト ID を生成する。
func generateRandomID() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// isBodyTooLarge は http.MaxBytesReader の上限を超えたエラーかを判定する。
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// parseMultipart は上限付きでリクエストを解析する。失敗時はレスポンスを書いて false を返す。
func parseMultipart(c *gin.Context, limit int64) bool {
	// 本文全体を limit バイトで打ち切り、巨大なリクエストでディスクやメモリを使い切られないようにする
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	if err := c.Request.ParseMultipartForm(multipartMemoryBytes); err != nil {
		if isBodyTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("Request body too large (max %d bytes)", limit)})
			return false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid multipart form: %v", err)})
		return false
	}
	return true
}

// sniffContentType は先頭 512 バイトから Content-Type を判定し、読み取り位置を先頭に戻す。
func sniffContentType(f multipart.File) (string, error) {
	buffer := make([]byte, 512)
	n, err := io.ReadFull(f, buffer)
	// 512 バイト未満の小さいファイルでは EOF 系のエラーになるが、読めた分で判定するので許容する
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return http.DetectContentType(buffer[:n]), nil
}

// openThumbnail は thumbnail フィールドを検証（サイズ・画像形式）して開く。
// ok=false のときはレスポンスを書き込み済み。present=false はフィールドが無い。
func openThumbnail(c *gin.Context) (header *multipart.FileHeader, f multipart.File, present bool, ok bool) {
	header, err := c.FormFile("thumbnail")
	if err == http.ErrMissingFile {
		return nil, nil, false, true
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to get thumbnail file: %v", err)})
		return nil, nil, true, false
	}
	if header.Size > maxImageUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("Thumbnail too large (max %d bytes)", maxImageUploadBytes)})
		return nil, nil, true, false
	}
	f, err = header.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open thumbnail file"})
		return nil, nil, true, false
	}
	// クライアント申告の Content-Type ではなく、実際の中身から形式を判定する
	ct, err := sniffContentType(f)
	if err != nil {
		f.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read thumbnail file"})
		return nil, nil, true, false
	}
	if !allowedThumbnailTypes[ct] {
		f.Close()
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": fmt.Sprintf("Unsupported thumbnail type: %s. Only JPEG, PNG, GIF and WebP are allowed.", ct)})
		return nil, nil, true, false
	}
	return header, f, true, true
}

// safeFilename はパス区切りを取り除いたファイル名を返す。
// Windows 形式の "\" も "/" とみなして末尾要素だけを残し、空や "." / ".." などは "upload" に置き換える。
func safeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		return "upload"
	}
	return name
}

// uploadToSFSP は src を multipart で sfsp-api にストリーミング送信する（全体をメモリに読まない）。
func uploadToSFSP(ctx context.Context, src io.Reader, filename, targetService string) (*SfspUploadResponse, error) {
	// 1. パイプの書き込み側で multipart 本文を組み立て、読み込み側を HTTP リクエストの本文にする
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		err := func() error {
			if err := mw.WriteField("target_service", targetService); err != nil {
				return fmt.Errorf("failed to write target_service: %w", err)
			}
			part, err := mw.CreateFormFile("file", safeFilename(filename))
			if err != nil {
				return fmt.Errorf("failed to create form: %w", err)
			}
			if _, err := io.Copy(part, src); err != nil {
				return fmt.Errorf("failed to copy file: %w", err)
			}
			return mw.Close()
		}()
		// 書き込み中のエラーを読み込み側（HTTP クライアント）に伝える。err が nil なら通常の EOF になる
		pw.CloseWithError(err)
	}()
	// 送信側の goroutine が src を読み終える（または中断する）まで待ってから戻る
	// （呼び出し側が戻った後に src が閉じられても goroutine が読み続けないようにするため）。
	// pr を先に閉じることで、書き込み途中の goroutine もエラーで抜けられる。
	defer func() {
		pr.CloseWithError(errors.New("sfsp upload finished"))
		<-writeDone
	}()

	// 2. sfsp-api へ送信する
	req, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", pr)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFSP request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-SFSP-API-Key", sfspApiKey)

	resp, err := sfspHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SFSP request failed: %w", err)
	}
	defer resp.Body.Close()

	// 3. 応答（最大 1MiB）を読み、2xx 以外はエラーにして job_id を取り出す
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

// uploadStaticSiteHandler は POST /api/static-sites/upload を処理する。
// ZIP（と任意のサムネイル画像）を受け取り、static_sites に 'scanning' 状態の行を作成したうえで
// sfsp-api にスキャンを依頼し、202 とサイト ID を返す。公開処理はスキャン完了後に別のワーカーが行う。
func uploadStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	userIDStr := c.GetString("userID")

	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID format"})
		return
	}

	// 1. サイト ZIP・サムネイル・フォーム項目の合計を上限としてリクエストを解析する
	if !parseMultipart(c, maxSiteUploadBytes+maxImageUploadBytes+multipartOverheadBytes) {
		return
	}

	// 2. フォーム項目を検証し、チーム投稿ならチームへの投稿権限を確認する
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

	// 3. サイト本体のファイルを取り出し、サイズを確認して開く
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to get file: %v", err)})
		return
	}
	if fileHeader.Size > maxSiteUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("File too large (max %d bytes)", maxSiteUploadBytes)})
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to open uploaded file: %v", err)})
		return
	}
	defer file.Close()

	// 4. ファイル形式チェック（中身から判定し、ZIP 以外は拒否する）
	contentType, err := sniffContentType(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if contentType != "application/zip" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": fmt.Sprintf("Unsupported file type: %s. Only ZIP files are allowed.", contentType)})
		return
	}

	// 5. 任意のサムネイルを検証して開く
	thumbnailHeader, thumbnailFile, hasThumbnail, ok := openThumbnail(c)
	if !ok {
		return
	}
	if hasThumbnail {
		defer thumbnailFile.Close()
	}

	// 6. サイト ID を採番する
	siteID, err := generateRandomID()
	if err != nil {
		logger.Errorf("ERROR: Failed to generate static site ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate unique ID"})
		return
	}

	// 公開投稿（team_id が空）は空文字ではなく NULL で保存する（一覧の team_id IS NULL 条件に合わせる）
	teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}

	// 7. 'scanning' 状態の行を作成する。
	// 行を先に作成し、sfsp-api の応答を受けたらすぐに job_id を書き込む。
	// こうすることで完了イベントが届いた時点で行が必ず存在する（残る隙間はワーカーの再試行で吸収する）。
	_, err = db.ExecContext(reqCtx,
		"INSERT INTO static_sites (id, user_id, title, description, status, minio_path, team_id, created_at, updated_at) VALUES ($1, $2, $3, $4, 'scanning', $5, $6, NOW(), NOW())",
		siteID, userUUID, title, description, "", teamIDVal)
	if err != nil {
		logger.Errorf("ERROR: Failed to insert static site into DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register static site"})
		return
	}
	// abort は以降の処理が失敗したときに作成済みの行を削除し、エラー応答を返す
	abort := func(status int, msg string) {
		// リクエストのコンテキストが切れていても削除できるよう独立したコンテキストを使う
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DELETE FROM static_sites WHERE id = $1", siteID); err != nil {
			logger.Errorf("ERROR: Failed to roll back static site %s: %v", siteID, err)
		}
		c.JSON(status, gin.H{"error": msg})
	}

	// 8. サムネイルがあれば先に sfsp-api へ送り、job_id を行に記録する
	if hasThumbnail {
		thumbResp, err := uploadToSFSP(reqCtx, thumbnailFile, thumbnailHeader.Filename, "site-thumbnail")
		if err != nil {
			logger.Errorf("Error forwarding thumbnail to SFSP: %v", err)
			abort(http.StatusServiceUnavailable, "Thumbnail scanning service is unavailable")
			return
		}
		thumbJobID, err := uuid.Parse(thumbResp.JobID)
		if err != nil {
			logger.Errorf("Invalid Thumbnail Job ID from SFSP: %v", err)
			abort(http.StatusInternalServerError, "Invalid thumbnail job ID from SFSP")
			return
		}
		if _, err := db.ExecContext(reqCtx, "UPDATE static_sites SET thumbnail_sfsp_job_id = $1 WHERE id = $2", thumbJobID, siteID); err != nil {
			logger.Errorf("ERROR: Failed to store thumbnail job ID: %v", err)
			abort(http.StatusInternalServerError, "Failed to register static site")
			return
		}
	}

	// 9. サイト ZIP を sfsp-api へ送り、job_id を行に記録する
	sfspRespData, err := uploadToSFSP(reqCtx, file, fileHeader.Filename, "static-site")
	if err != nil {
		logger.Errorf("Error forwarding file to SFSP: %v", err)
		abort(http.StatusServiceUnavailable, "File scanning service is unavailable")
		return
	}
	sfspJobID, err := uuid.Parse(sfspRespData.JobID)
	if err != nil {
		logger.Errorf("Invalid Job ID from SFSP: %v (job_id=%q)", err, sfspRespData.JobID)
		abort(http.StatusInternalServerError, "Invalid Job ID from SFSP")
		return
	}
	if _, err := db.ExecContext(reqCtx, "UPDATE static_sites SET sfsp_job_id = $1 WHERE id = $2", sfspJobID, siteID); err != nil {
		logger.Errorf("ERROR: Failed to store SFSP job ID: %v", err)
		abort(http.StatusInternalServerError, "Failed to register static site")
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "Static site upload initiated for scanning", "siteId": siteID})
}

// listStaticSitesHandler は GET /api/static-sites を処理し、公開済みの静的サイト一覧を新しい順に返す。
// クエリ q でタイトルの部分一致検索、team でチーム限定の一覧に切り替える（認証は任意）。
func listStaticSitesHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	searchTerm := c.Query("q")
	teamToken := c.Query("team")

	// team パラメータ: ある場合はそのチーム限定(team_id = $)、なければ公開(team_id IS NULL)のみ。
	// status='public' のみを表示。プレースホルダーは args の順序に合わせて番号を振る。
	whereConds := []string{"status = 'public'"}
	var args []interface{}
	argIdx := 1
	if teamToken != "" {
		// チームの投稿一覧は閲覧権限（非公開チームならメンバー）が必要
		viewerID, _ := optionalUser(c)
		perm, err := fetchTeamPermission(reqCtx, teamToken, viewerAuthHeader(c, viewerID))
		if err != nil {
			logger.Errorf("listStaticSitesHandler: team permission check failed: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to verify team permission"})
			return
		}
		if !perm.CanView {
			c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to view this team's content"})
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

	// 条件の文字列は固定の断片だけで組み立て、利用者の入力は args でプレースホルダーに渡す
	rows, err := db.QueryContext(reqCtx,
		"SELECT id, user_id, title, description, minio_path, status, entry_point_path, thumbnail_url, created_at, updated_at FROM static_sites "+whereClause+" ORDER BY created_at DESC", args...)
	if err != nil {
		logger.Errorf("ERROR: Database query failed in listStaticSitesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve static sites"})
		return
	}
	defer rows.Close()

	var staticSites []StaticSite
	for rows.Next() {
		var s StaticSite
		var description sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.Title, &description, &s.MinioPath, &s.Status, &s.EntryPointPath, &s.ThumbnailURL, &s.CreatedAt, &s.UpdatedAt); err != nil {
			// 読み取れない行はログに残して読み飛ばす
			logger.Errorf("Error scanning static site row: %v", err)
			continue
		}
		s.Description = description.String
		// SELECT では team_id を取得していないため、チーム限定の一覧では検索条件のトークンを詰める
		if teamToken != "" {
			s.TeamID = teamToken
		}
		staticSites = append(staticSites, s)
	}
	c.JSON(http.StatusOK, staticSites)
}

// getStaticSiteHandler は GET /api/static-sites/:id を処理し、静的サイト1件の詳細を返す（認証は任意）。
// 閲覧権限がない場合は存在自体を隠すため 404 を返す。
func getStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	var s StaticSite
	var description, teamID sql.NullString
	// 1. 行を取得する（権限確認の前に取得し、存在しない場合は 404）
	err := db.QueryRowContext(reqCtx, "SELECT id, user_id, team_id, title, description, minio_path, status, processing_details, entry_point_path, thumbnail_url, created_at, updated_at, thumbnail_sfsp_job_id FROM static_sites WHERE id = $1", siteID).Scan(
		&s.ID, &s.UserID, &teamID, &s.Title, &description, &s.MinioPath, &s.Status, &s.ProcessingDetails, &s.EntryPointPath, &s.ThumbnailURL, &s.CreatedAt, &s.UpdatedAt, &s.ThumbnailSfspJobID)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		} else {
			logger.Errorf("ERROR: Database query failed in getStaticSiteHandler: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve static site"})
		}
		return
	}
	s.Description = description.String
	s.TeamID = teamID.String

	// 2. 閲覧権限: 投稿者（と管理者）は常に閲覧可。それ以外は status='public' のみ、
	// チーム限定ならチームの閲覧権限（can_view）が必要。存在を漏らさないため 404 を返す。
	userID, isAdmin := optionalUser(c)
	isOwner := userID != "" && strings.EqualFold(userID, s.UserID)
	if !isOwner && !isAdmin {
		if s.Status != "public" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
			return
		}
		if s.TeamID != "" {
			perm, err := fetchTeamPermission(reqCtx, s.TeamID, viewerAuthHeader(c, userID))
			if err != nil {
				logger.Errorf("getStaticSiteHandler: team permission check failed: %v", err)
				c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to verify team permission"})
				return
			}
			if !perm.CanView {
				c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
				return
			}
		}
		// 投稿者以外には内部情報を返さない
		s.ProcessingDetails = nil
		s.ThumbnailSfspJobID = nil
	}
	c.JSON(http.StatusOK, s)
}

// updateStaticSiteHandler は PUT /api/static-sites/:id を処理する（投稿者または管理者のみ）。
// サムネイルが無ければタイトル・説明を即時更新して 200 を返す。サムネイルがある場合は
// タイトル・説明は更新せず、サムネイルを sfsp-api に送ってスキャン用の job_id を 202 で返す。
func updateStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	if !parseMultipart(c, maxImageUploadBytes+multipartOverheadBytes) {
		return
	}

	title := c.PostForm("title")
	description := c.PostForm("description")

	// 所有者を確認するため、現在のサイト情報を取得する
	var ownerUUID uuid.UUID
	err := db.QueryRowContext(reqCtx, "SELECT user_id FROM static_sites WHERE id = $1", siteID).Scan(&ownerUUID)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		} else {
			logger.Errorf("ERROR: Database query failed to get owner for site %s: %v", siteID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check ownership"})
		}
		return
	}

	// 権限チェック（管理者でも投稿者でもなければ拒否する）
	if !isAdmin && ownerUUID.String() != userIDStr {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to update this static site"})
		return
	}

	// サムネイルファイルを取得する。サムネイルがアップロードされた場合、タイトル・説明の
	// 更新はセキュリティスキャン完了まで保留する（フロントエンドがポーリング後に改めて
	// リクエストを送る）。サムネイルが無い場合は、タイトル・説明をすぐに更新する。
	thumbnailHeader, thumbnailFile, hasThumbnail, ok := openThumbnail(c)
	if !ok {
		return
	}
	if !hasThumbnail {
		_, err = db.ExecContext(reqCtx,
			"UPDATE static_sites SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
			title, description, siteID)
		if err != nil {
			logger.Errorf("ERROR: Failed to update static site %s in DB: %v", siteID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Static site updated successfully"})
		return
	}
	defer thumbnailFile.Close()

	// サムネイルを sfsp-api に送り、job_id を行に記録する（反映はスキャン完了後にワーカーが行う）
	thumbSfspResp, err := uploadToSFSP(reqCtx, thumbnailFile, thumbnailHeader.Filename, "site-thumbnail")
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

	_, err = db.ExecContext(reqCtx,
		"UPDATE static_sites SET thumbnail_sfsp_job_id = $1 WHERE id = $2",
		parsedThumbJobID, siteID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update static site thumbnail SFSP job ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site thumbnail"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Thumbnail upload accepted for scanning",
		"job_id":  parsedThumbJobID.String(),
		"status":  "scanning",
	})
}

// deleteStaticSiteHandler は DELETE /api/static-sites/:id を処理する（投稿者または管理者のみ）。
// MinIO 上のサムネイルとサイトのファイル一式を削除してから DB の行を削除する。
func deleteStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	// 所有者とサムネイル URL を取得し、投稿者または管理者であることを確認する
	var ownerUUID uuid.UUID
	var thumbnailURL sql.NullString
	err := db.QueryRowContext(reqCtx, "SELECT user_id, thumbnail_url FROM static_sites WHERE id = $1", siteID).Scan(&ownerUUID, &thumbnailURL)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		} else {
			logger.Errorf("ERROR: Database query failed to get owner for site %s: %v", siteID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check ownership"})
		}
		return
	}

	if !isAdmin && ownerUUID.String() != userIDStr {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this static site"})
		return
	}

	// 1. サムネイル画像が存在する場合、MinIOから削除
	if thumbnailURL.Valid && thumbnailURL.String != "" {
		// "/static-sites/thumbnails/xxxx.ext" から MinIO上のオブジェクトパス "thumbnails/xxxx.ext" を抽出
		parts := strings.Split(thumbnailURL.String, "/static-sites/")
		if len(parts) == 2 {
			thumbObjPath := parts[1]
			// サムネイル削除の失敗は無視し、サイト本体の削除を続ける
			_ = minioClient.RemoveObject(reqCtx, minioBucket, thumbObjPath, minio.RemoveObjectOptions{})
		}
	}

	// 2. サイトの静的コンテンツファイルを MinIO から削除
	objectPrefix := fmt.Sprintf("%s/", siteID)
	objectsCh := make(chan minio.ObjectInfo)

	// "<siteID>/" 配下のオブジェクトを列挙して objectsCh に流し、RemoveObjects で一括削除する
	// （列挙でエラーが出た場合はそこで打ち切る）
	go func() {
		defer close(objectsCh)
		for object := range minioClient.ListObjects(reqCtx, minioBucket, minio.ListObjectsOptions{
			Prefix:    objectPrefix,
			Recursive: true,
		}) {
			if object.Err != nil {
				logger.Errorf("ERROR: Failed to list object for deletion in MinIO: %v", object.Err)
				return
			}
			objectsCh <- object
		}
	}()

	errorCh := minioClient.RemoveObjects(reqCtx, minioBucket, objectsCh, minio.RemoveObjectsOptions{})
	// 削除エラーが1件でもあれば 500 を返し、DB の行は残す
	for err := range errorCh {
		logger.Errorf("ERROR: Failed to delete object %s from MinIO: %v", err.ObjectName, err.Err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete site files from storage"})
		return
	}

	// 3. DBからレコードを削除
	_, err = db.ExecContext(reqCtx, "DELETE FROM static_sites WHERE id = $1", siteID)
	if err != nil {
		logger.Errorf("ERROR: Failed to delete static site %s from DB: %v", siteID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete static site from database"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Static site deleted successfully"})
}

// deleteThumbnailHandler は DELETE /api/static-sites/:id/thumbnail を処理する（投稿者または管理者のみ）。
// MinIO 上のサムネイル画像を削除し、thumbnail_url を NULL に戻す。
func deleteThumbnailHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	// 1. 所有者とサムネイル URL を取得し、権限を確認する
	var ownerUUID uuid.UUID
	var thumbnailURL sql.NullString
	err := db.QueryRowContext(reqCtx, "SELECT user_id, thumbnail_url FROM static_sites WHERE id = $1", siteID).Scan(&ownerUUID, &thumbnailURL)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		} else {
			logger.Errorf("ERROR: Database query failed to get owner for site %s: %v", siteID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check ownership"})
		}
		return
	}

	if !isAdmin && ownerUUID.String() != userIDStr {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to delete this thumbnail"})
		return
	}

	// 2. MinIO からサムネイルを削除する（URL の "/static-sites/" 以降をオブジェクトパスとみなす）。
	// サイト削除時と違い、こちらは削除に失敗したら 500 を返す
	if thumbnailURL.Valid && thumbnailURL.String != "" {
		parts := strings.Split(thumbnailURL.String, "/static-sites/")
		if len(parts) == 2 {
			thumbObjPath := parts[1]
			err := minioClient.RemoveObject(reqCtx, minioBucket, thumbObjPath, minio.RemoveObjectOptions{})
			if err != nil {
				logger.Errorf("Failed to delete thumbnail from MinIO: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete thumbnail from storage"})
				return
			}
		}
	}

	// 3. DB の thumbnail_url を NULL に戻す
	_, err = db.ExecContext(reqCtx, "UPDATE static_sites SET thumbnail_url = NULL WHERE id = $1", siteID)
	if err != nil {
		logger.Errorf("Failed to update thumbnail_url in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Thumbnail deleted successfully"})
}

// uploadLimitMiddleware はユーザーごとのアップロード制限（shared/ratelimit）をかける。authMiddleware の後に置く。
// ゲーム・静的サイト・動画で同じ Redis のキーを使うので、本数は 3 種類の合計で数える。
// 管理者は制限しない。Redis に届かないときはログを出して通す（サーバー全体の同時数は nginx でも制限している）。
func uploadLimitMiddleware(limits ratelimit.UploadLimits) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool("isAdmin") {
			c.Next()
			return
		}
		userID := c.GetString("userID")
		d, err := ratelimit.CheckUpload(c.Request.Context(), queue.RedisClient, limits, userID)
		if err != nil {
			logger.Warnf("Upload rate limit check failed, allowing the request: %v", err)
		}
		if !d.Allowed {
			logger.Warnf("Upload rejected by per-user rate limit: user=%s reason=%s", userID, d.Reason)
			c.Header("Retry-After", strconv.Itoa(int(d.RetryAfter/time.Second)+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": d.Reason})
			return
		}
		defer d.Release()
		c.Next()
	}
}
