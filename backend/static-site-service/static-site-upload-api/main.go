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
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

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
	if !teamTokenPattern.MatchString(teamToken) {
		return &teamPermission{}, nil
	}
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
	if resp.StatusCode == http.StatusNotFound {
		return &teamPermission{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("team service returned %d for team %s", resp.StatusCode, teamToken)
	}
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

// teamAccessCheck calls the team-service permission endpoint to verify that the
// caller is allowed to post to the given team. Returns true if posting is allowed.
// A missing/empty team_id is treated as a public post (always allowed).
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

// StaticSite represents a static site entry in the database.
type StaticSite struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"user_id"`
	TeamID             string     `json:"team_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	MinioPath          string     `json:"minio_path"`
	EntryPointPath     string     `json:"entry_point_path"`
	ThumbnailURL       *string    `json:"thumbnail_url"`
	Status             string     `json:"status"`
	ProcessingDetails  *string    `json:"processing_details,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ThumbnailSfspJobID *uuid.UUID `json:"thumbnail_sfsp_job_id,omitempty"`
}

// SfspUploadResponse は sfsp-api の POST /api/v1/files の応答（使うのは job_id だけ）。
// スキャン結果は常に完了イベントで届く（sfsp-api は status "queued" しか返さない）。
type SfspUploadResponse struct {
	JobID string `json:"job_id"`
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
	if v := os.Getenv("TEAM_SERVICE_URL"); v != "" {
		teamServiceURL = strings.TrimRight(v, "/")
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

	// バケットの作成と匿名ダウンロードの設定は minio-init が行う

	if err := queue.ConnectRedis(cfg, logger); err != nil {
		logger.Fatalf("Failed to connect to Redis: %v", err)
	}

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

	r := gin.Default()
	r.MaxMultipartMemory = multipartMemoryBytes

	r.GET("/api/static-sites", listStaticSitesHandler)
	r.GET("/api/static-sites/:id", getStaticSiteHandler)

	auth := r.Group("/")
	auth.Use(authMiddleware)
	{
		auth.POST("/api/static-sites/upload", uploadStaticSiteHandler)
		auth.PUT("/api/static-sites/:id", updateStaticSiteHandler)
		auth.DELETE("/api/static-sites/:id", deleteStaticSiteHandler)
		auth.DELETE("/api/static-sites/:id/thumbnail", deleteThumbnailHandler)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	logger.Infof("Static Site Upload API running on port %s", port)
	logger.Fatal(r.Run(":" + port))
}

// authenticate は Authorization ヘッダーの JWT を検証し、ブロックリスト（blocklist:<jti>）を確認する。
// status が 0 なら成功。Redis のエラー時は拒否する（Fail-Closed）。
func authenticate(c *gin.Context) (userID string, isAdmin bool, status int, msg string) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false, http.StatusUnauthorized, "Authorization header required"
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")

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

// authMiddleware authenticates requests using JWT.
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
func safeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		return "upload"
	}
	return name
}

// uploadToSFSP は src を multipart で sfsp-api にストリーミング送信する（全体をメモリに読まない）。
func uploadToSFSP(ctx context.Context, src io.Reader, filename, targetService string) (*SfspUploadResponse, error) {
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
		pw.CloseWithError(err)
	}()
	// 送信側の goroutine が src を読み終える（または中断する）まで待ってから戻る
	defer func() {
		pr.CloseWithError(errors.New("sfsp upload finished"))
		<-writeDone
	}()

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

func uploadStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	userIDStr := c.GetString("userID")

	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID format"})
		return
	}

	if !parseMultipart(c, maxSiteUploadBytes+maxImageUploadBytes+multipartOverheadBytes) {
		return
	}

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

	// ファイル形式チェック
	contentType, err := sniffContentType(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if contentType != "application/zip" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": fmt.Sprintf("Unsupported file type: %s. Only ZIP files are allowed.", contentType)})
		return
	}

	thumbnailHeader, thumbnailFile, hasThumbnail, ok := openThumbnail(c)
	if !ok {
		return
	}
	if hasThumbnail {
		defer thumbnailFile.Close()
	}

	siteID, err := generateRandomID()
	if err != nil {
		logger.Errorf("ERROR: Failed to generate static site ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate unique ID"})
		return
	}

	teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}

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
	abort := func(status int, msg string) {
		// リクエストのコンテキストが切れていても削除できるよう独立したコンテキストを使う
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DELETE FROM static_sites WHERE id = $1", siteID); err != nil {
			logger.Errorf("ERROR: Failed to roll back static site %s: %v", siteID, err)
		}
		c.JSON(status, gin.H{"error": msg})
	}

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
			logger.Errorf("Error scanning static site row: %v", err)
			continue
		}
		s.Description = description.String
		if teamToken != "" {
			s.TeamID = teamToken
		}
		staticSites = append(staticSites, s)
	}
	c.JSON(http.StatusOK, staticSites)
}

func getStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	var s StaticSite
	var description, teamID sql.NullString
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

	// 閲覧権限: 投稿者（と管理者）は常に閲覧可。それ以外は status='public' のみ、
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

	// Fetch current site details to check ownership
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

	// Authorization check
	if !isAdmin && ownerUUID.String() != userIDStr {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to update this static site"})
		return
	}

	// Fetch the thumbnail file. When a thumbnail is uploaded, the title/description
	// update is deferred until the security scan completes (the frontend sends a
	// follow-up request after polling). When no thumbnail is uploaded, update
	// title/description immediately.
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

func deleteStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

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
			_ = minioClient.RemoveObject(reqCtx, minioBucket, thumbObjPath, minio.RemoveObjectOptions{})
		}
	}

	// 2. サイトの静的コンテンツファイルを MinIO から削除
	objectPrefix := fmt.Sprintf("%s/", siteID)
	objectsCh := make(chan minio.ObjectInfo)

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

func deleteThumbnailHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

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

	_, err = db.ExecContext(reqCtx, "UPDATE static_sites SET thumbnail_url = NULL WHERE id = $1", siteID)
	if err != nil {
		logger.Errorf("Failed to update thumbnail_url in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Thumbnail deleted successfully"})
}
