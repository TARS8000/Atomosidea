package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// サービス全体で共有する接続・設定値。main() の起動時に環境変数から初期化される。
//   - dbAuth: auth-db（users テーブル）への接続プール
//   - dbApp: app-db（games / static_sites / videos）への接続プール。アカウント削除時に使う
//   - redisClient: JWT ブロックリストと OAuth ワンタイムコードの保存先
//   - minioClient*: プロフィール画像・ゲーム・静的サイト・動画それぞれの MinIO クライアント
//   - jwtSecret: JWT の HS256 署名鍵
//   - googleOauthConfig: Google OAuth2 設定（クライアント ID/シークレット未設定なら nil）
//   - appURL: フロントエンドの公開 URL（CORS の許可オリジンと Google のリダイレクト URL に使う）
//   - adminRegistrationCode: ローカル管理者アカウントの登録に必要なコード
//   - internalAPIKey: サービス間呼び出しで X-Internal-Key ヘッダーに載せるキー
//   - profileServiceURL / teamServiceURL: 各サービスのベース URL（末尾の / は除去済み）
//   - cookieSecure: APP_URL が https のとき true にし、Cookie に Secure 属性を付ける
//   - *BucketName: 各 MinIO バケット名
var (
	dbAuth                *pgxpool.Pool
	dbApp                 *pgxpool.Pool
	redisClient           *redis.Client
	minioClientProfile    *minio.Client
	minioClientGame       *minio.Client
	minioClientStaticSite *minio.Client
	minioClientVideo      *minio.Client // VIDEO_MINIO_* の認証情報が未設定の場合は nil
	jwtSecret             []byte
	googleOauthConfig     *oauth2.Config
	appURL                string
	adminRegistrationCode string
	internalAPIKey        string
	profileServiceURL     string
	teamServiceURL        string
	cookieSecure          bool
	profileBucketName     string = "user-profiles"
	gameBucketName        string = "games"
	staticSiteBucketName  string = "static-sites"
	videoBucketName       string = "videos"

	// internalHTTPClient はサービス間呼び出し（profile-service、team-service、Google）に使う。
	internalHTTPClient = &http.Client{Timeout: 15 * time.Second}
)

// OAuth の state Cookie、ワンタイムコード、管理者パスワードに関する定数。
// state Cookie の Path を /api/auth/google に限定し、Google ログイン関連のパスにだけ送られるようにしている。
const oauthStateCookieName = "oauthstate"
const oauthStateCookiePath = "/api/auth/google"
const oauthStateMaxAge = 10 * 60 // 秒
const oauthCodeKeyPrefix = "oauth_code:"
const oauthCodeTTL = 60 * time.Second
const minAdminPasswordLength = 8

// errAccountDisabled は、ユーザーは存在するが status がログインを許可しない場合に返すエラー。
var errAccountDisabled = errors.New("account is disabled")

// isLoginAllowedStatus は users.status の値がログイン／トークン利用を許可されるかを返す。
// auth-db での値は 'active' / 'pending' / 'deleted_data'。'deleted_data'（および未知の値）のみ拒否する。
// 'pending' は旧バージョンで作成された行のために許可したままにしている。
func isLoginAllowedStatus(status string) bool {
	return status == "active" || status == "pending"
}

// randomURLToken は暗号学的乱数で n バイトを生成し、base64url（パディングなし）で返す。
func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// User 構造体（ID を uuid.UUID に設定）
// auth-db の users テーブルの1行のうち、ログイン処理で使う列を保持する。
// Username は Google ユーザーのデータ削除後に NULL になり得るため sql.NullString にしている。
type User struct {
	ID           uuid.UUID      `json:"id"`
	Username     sql.NullString `json:"username"`
	Email        string         `json:"email"`
	PasswordHash string         `json:"-"`
	IsAdmin      bool           `json:"is_admin"`
}

// authMiddleware は Authorization ヘッダーの Bearer トークン（JWT）を検証する Gin ミドルウェアを返す。
// 署名・有効期限の検証、ブロックリスト照合、users.status の確認を行い、
// 通過したらコンテキストに userID / userUUID / isAdmin / tokenString を格納する。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Authorization ヘッダーからトークン文字列を取り出す
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		// 2. 署名と有効期限を検証する（alg 差し替え攻撃を防ぐため HS256 のみ許可）
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			// 3. トークンがブロックリストに登録されていないか確認する（Redis エラー時は拒否する Fail-Closed）
			// logoutHandler は "blocklist:<jti>" で登録するため、同じキーで照合する
			jti, _ := claims["jti"].(string)
			if jti == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
				return
			}
			exists, err := redisClient.Exists(c.Request.Context(), fmt.Sprintf("blocklist:%s", jti)).Result()
			if err != nil {
				log.Printf("Redis error checking blocklist: %v", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
				return
			}
			if exists > 0 {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
				return
			}

			// 4. トークンからユーザー ID を取り出し UUID として解釈する
			// JWTからのID取得を文字列（UUID形式）として処理
			// 互換性のため sub → userID → user_id の順にクレームを探す
			var userIDStr string
			if idStr, ok := claims["sub"].(string); ok {
				userIDStr = idStr
			} else if idStr, ok := claims["userID"].(string); ok {
				userIDStr = idStr
			} else if idStr, ok := claims["user_id"].(string); ok {
				userIDStr = idStr
			} else {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
				return
			}

			parsedUUID, err := uuid.Parse(userIDStr)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid UUID format in token"})
				return
			}

			// 5. 削除済み・無効なアカウントのトークンを拒否する（DB エラー時も拒否する Fail-Closed）
			var status string
			err = dbAuth.QueryRow(c.Request.Context(), "SELECT status FROM users WHERE id = $1", parsedUUID).Scan(&status)
			if err == pgx.ErrNoRows {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
				return
			}
			if err != nil {
				log.Printf("Database error checking user status: %v", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
				return
			}
			if !isLoginAllowedStatus(status) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Account is disabled"})
				return
			}

			// 6. 後続ハンドラ向けにユーザー情報をコンテキストへ格納する
			// isAdmin は DB ではなく JWT のクレームから取る（クレームがなければ false）
			isAdmin, ok := claims["isAdmin"].(bool)
			if !ok {
				isAdmin = false
			}
			c.Set("userID", parsedUUID.String()) // 文字列のUUIDとして格納
			c.Set("userUUID", parsedUUID)        // uuid.UUID型としても格納
			c.Set("isAdmin", isAdmin)
			c.Set("tokenString", tokenString)
			c.Next()
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		}
	}
}

// main は環境変数から設定を読み込み、Redis・PostgreSQL（auth-db / app-db）・MinIO に接続したうえで、
// /api/auth 以下のルートを登録して :8080 で HTTP サーバーを起動する。
func main() {
	var err error
	// 1. 環境変数から設定値を読み込む（未設定のサービス URL は Docker Compose 内の既定値を使う）
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	authDatabaseUrl := os.Getenv("DATABASE_URL")
	appDatabaseUrl := os.Getenv("APP_DATABASE_URL")
	appURL = os.Getenv("APP_URL")
	adminRegistrationCode = os.Getenv("ADMIN_REGISTRATION_CODE")
	redisAddr := os.Getenv("REDIS_ADDR")
	redisPassword := os.Getenv("REDIS_PASSWORD")
	redisDB := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			redisDB = n
		} else {
			log.Printf("WARNING: invalid REDIS_DB %q, using 0", v)
		}
	}
	internalAPIKey = os.Getenv("INTERNAL_API_KEY")
	if internalAPIKey == "" {
		log.Println("WARNING: INTERNAL_API_KEY is not set; internal calls to profile-service / team-service will be rejected.")
	}
	profileServiceURL = strings.TrimRight(os.Getenv("PROFILE_SERVICE_URL"), "/")
	if profileServiceURL == "" {
		profileServiceURL = "http://profile-service:8084"
	}
	teamServiceURL = strings.TrimRight(os.Getenv("TEAM_SERVICE_URL"), "/")
	if teamServiceURL == "" {
		teamServiceURL = "http://team-service:8080"
	}
	cookieSecure = strings.HasPrefix(strings.ToLower(appURL), "https://")

	// 2. Redis クライアントを初期化する（起動順の差を吸収するため 5 秒間隔で最大 5 回試行）
	for i := 0; i < 5; i++ {
		redisClient = redis.NewClient(&redis.Options{Addr: redisAddr, Password: redisPassword, DB: redisDB})
		_, err = redisClient.Ping(context.Background()).Result()
		if err == nil {
			break
		}
		log.Printf("Failed to connect to Redis, retrying in 5 seconds... (%d/5)", i+1)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Unable to connect to Redis after multiple retries: %v", err)
	}
	log.Println("Successfully connected to Redis!")

	// 3. MinIO の接続情報を読み込む（動画用エンドポイントが未設定なら共通の MINIO_ENDPOINT を使う）
	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	profileMinioAccessKey := os.Getenv("PROFILE_MINIO_ACCESS_KEY_ID")
	profileMinioSecretKey := os.Getenv("PROFILE_MINIO_SECRET_ACCESS_KEY")
	gameMinioEndpoint := os.Getenv("GAME_MINIO_ENDPOINT")
	gameMinioAccessKey := os.Getenv("GAME_MINIO_ACCESS_KEY_ID")
	gameMinioSecretKey := os.Getenv("GAME_MINIO_SECRET_ACCESS_KEY")
	staticSiteMinioEndpoint := os.Getenv("STATIC_SITE_MINIO_ENDPOINT")
	staticSiteMinioAccessKey := os.Getenv("STATIC_SITE_MINIO_ACCESS_KEY_ID")
	staticSiteMinioSecretKey := os.Getenv("STATIC_SITE_MINIO_SECRET_ACCESS_KEY")
	videoMinioEndpoint := os.Getenv("VIDEO_MINIO_ENDPOINT")
	if videoMinioEndpoint == "" {
		videoMinioEndpoint = minioEndpoint
	}
	videoMinioAccessKey := os.Getenv("VIDEO_MINIO_ACCESS_KEY_ID")
	videoMinioSecretKey := os.Getenv("VIDEO_MINIO_SECRET_ACCESS_KEY")
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"

	// 4. auth-db に接続する（5 秒間隔で最大 5 回試行）
	for i := 0; i < 5; i++ {
		dbAuth, err = pgxpool.New(context.Background(), authDatabaseUrl)
		if err == nil {
			err = dbAuth.Ping(context.Background())
			if err == nil {
				break
			}
		}
		log.Printf("Failed to connect to auth database, retrying in 5 seconds... (%d/5)", i+1)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Unable to connect to auth database after multiple retries: %v", err)
	}
	defer dbAuth.Close()
	log.Println("Successfully connected to auth PostgreSQL!")

	// 5. app-db に接続する（5 秒間隔で最大 5 回試行）
	for i := 0; i < 5; i++ {
		dbApp, err = pgxpool.New(context.Background(), appDatabaseUrl)
		if err == nil {
			err = dbApp.Ping(context.Background())
			if err == nil {
				break
			}
		}
		log.Printf("Failed to connect to app database, retrying in 5 seconds... (%d/5)", i+1)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Unable to connect to app database after multiple retries: %v", err)
	}
	defer dbApp.Close()
	log.Println("Successfully connected to app PostgreSQL!")

	// 6. 用途別の MinIO クライアントを初期化する
	// プロフィール画像用（minio.New は接続確認をしないため、ここで失敗するのは設定不備の場合）
	minioClientProfile, err = minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(profileMinioAccessKey, profileMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to profile MinIO: %v", err)
	}

	// ゲーム用
	minioClientGame, err = minio.New(gameMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(gameMinioAccessKey, gameMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to game MinIO: %v", err)
	}

	// 静的サイト用
	minioClientStaticSite, err = minio.New(staticSiteMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(staticSiteMinioAccessKey, staticSiteMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to static site MinIO: %v", err)
	}

	// 動画用（HLS とサムネイル）。アカウント削除時に使う。認証情報がなければ作成せず nil のままにする
	if videoMinioAccessKey != "" && videoMinioSecretKey != "" && videoMinioEndpoint != "" {
		minioClientVideo, err = minio.New(videoMinioEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(videoMinioAccessKey, videoMinioSecretKey, ""),
			Secure: minioUseSSL,
		})
		if err != nil {
			log.Fatalf("Unable to connect to video MinIO: %v", err)
		}
	} else {
		log.Println("WARNING: VIDEO_MINIO_ACCESS_KEY_ID / VIDEO_MINIO_SECRET_ACCESS_KEY not set; video objects will not be deleted on account deletion.")
	}

	// 7. Gin ルーターを作成し、CORS は APP_URL のオリジンのみ許可する
	r := gin.Default()
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{appURL}
	config.AllowMethods = []string{"GET", "POST", "OPTIONS", "DELETE"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	r.Use(cors.New(config))

	// 8. /api/auth 以下のルートを登録する（logout と /me は認証必須）
	authRoutes := r.Group("/api/auth")
	{
		authRoutes.POST("/register", registerHandler)
		authRoutes.POST("/login", loginHandler)
		authRoutes.POST("/logout", authMiddleware(), logoutHandler)
		authRoutes.POST("/oauth/exchange", oauthExchangeHandler)

		// Google のクライアント ID とシークレットが両方設定されている場合のみ Google ログインを有効にする
		googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
		googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
		if googleClientID != "" && googleClientSecret != "" {
			googleOauthConfig = &oauth2.Config{
				RedirectURL:  fmt.Sprintf("%s/api/auth/google/callback", appURL),
				ClientID:     googleClientID,
				ClientSecret: googleClientSecret,
				Scopes: []string{
					"https://www.googleapis.com/auth/userinfo.email",
					"https://www.googleapis.com/auth/userinfo.profile",
				},
				Endpoint: google.Endpoint,
			}
			authRoutes.GET("/google/login", googleLoginHandler)
			authRoutes.GET("/google/callback", googleCallbackHandler)
			log.Println("INFO: Google OAuth2 has been enabled.")
		} else {
			log.Println("WARNING: Google OAuth2 is disabled.")
		}
		authRoutes.GET("/me", authMiddleware(), getMeHandler)
		authRoutes.DELETE("/me", authMiddleware(), deleteAccountHandler)
	}

	// 9. HTTP サーバーを起動する
	log.Println("INFO: Auth service starting on port 8080")
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（本文は画像程度までなので読み取りは短め）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// logoutHandler は POST /api/auth/logout を処理する。
// 現在のトークンの jti を、トークンの残り有効期間を TTL として Redis のブロックリストに登録し、以後の利用を無効化する。
func logoutHandler(c *gin.Context) {
	tokenString, exists := c.Get("tokenString")
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token not found in context"})
		return
	}

	// 署名は authMiddleware で検証済みのため、ここでは検証なしでクレームだけを取り出す
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString.(string), jwt.MapClaims{})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token format"})
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token claims"})
		return
	}

	jti, ok := claims["jti"].(string)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jti not found in token"})
		return
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "exp not found in token"})
		return
	}

	// 期限切れ後はブロックリストに残す必要がないため、TTL はトークンの残り有効期間にする
	ttl := time.Until(time.Unix(int64(exp), 0))
	if ttl <= 0 {
		c.JSON(http.StatusOK, gin.H{"message": "Token already expired"})
		return
	}

	err = redisClient.Set(c.Request.Context(), fmt.Sprintf("blocklist:%s", jti), true, ttl).Err()
	if err != nil {
		log.Printf("Redis error setting blocklist: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to invalidate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Successfully logged out"})
}

// registerHandler は POST /api/auth/register を処理し、ローカル（メール＋パスワード）の管理者アカウントを作成する。
// 正しい管理者コードがなければ拒否するため、一般ユーザーのローカル登録はできない。
// 作成後は profile-service に初期プロフィールの作成を依頼する。
func registerHandler(c *gin.Context) {
	var req struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		AdminCode string `json:"adminCode"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("DEBUG: registerHandler JSON bind error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request format: %v", err)})
		return
	}

	// ローカルアカウント登録は管理者コードを入力した場合（管理者アカウント）のみ許可
	// (入力されたコードはログに残さない)
	// タイミング攻撃でコードを推測されないよう定数時間比較を使う。サーバー側のコードが未設定なら常に拒否する
	if adminRegistrationCode == "" || subtle.ConstantTimeCompare([]byte(req.AdminCode), []byte(adminRegistrationCode)) != 1 {
		log.Printf("WARN: Rejected admin registration attempt with invalid admin code")
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid Admin Code."})
		return
	}

	// メールアドレス・パスワードは必須（既定のパスワードは使わない）
	email := strings.ToLower(strings.TrimSpace(req.Email))
	password := req.Password
	if !strings.Contains(email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A valid email is required."})
		return
	}
	if len(password) < minAdminPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Password must be at least %d characters.", minAdminPasswordLength)})
		return
	}

	// 同じメールアドレスのローカルアカウントが既に存在しないか確認する
	var existingID uuid.UUID
	err := dbAuth.QueryRow(context.Background(), "SELECT id FROM users WHERE email = $1 AND provider = 'local'", email).Scan(&existingID)
	if err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "An account with this email already exists."})
		return
	}
	if err != pgx.ErrNoRows {
		log.Printf("Database error while checking for existing account: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error while checking for existing account."})
		return
	}

	// パスワードのハッシュ化
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	// UUID v7 の手動生成
	newID, err := uuid.NewV7()
	if err != nil {
		log.Printf("Failed to generate UUIDv7: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate user ID"})
		return
	}

	// ユーザー名はメールアドレスのローカル部（プロフィール画面で変更可能）
	username := strings.SplitN(email, "@", 2)[0]

	// is_admin = true、status = 'active' でユーザー行を作成する
	var userID uuid.UUID
	err = dbAuth.QueryRow(context.Background(),
		"INSERT INTO users (id, username, email, password_hash, provider, is_admin, status) VALUES ($1, $2, $3, $4, 'local', $5, 'active') RETURNING id",
		newID, username, email, string(hashedPassword), true).Scan(&userID)
	if err != nil {
		log.Printf("Failed to create admin user: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create admin user. Email might already be in use."})
		return
	}

	// profile-service へ管理者の初期プロフィール作成を同期で要求する（失敗時はログ出力）
	if err := createProfile(c.Request.Context(), userID.String(), username); err != nil {
		log.Printf("ERROR: Failed to create profile for admin %s: %v", userID.String(), err)
	} else {
		log.Printf("INFO: Profile created for admin %s", userID.String())
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Admin user created successfully", "userID": userID.String()})
}

// loginHandler は POST /api/auth/login を処理する。
// ローカルアカウントのメールアドレスとパスワードを照合し、成功すれば JWT を {"token": ...} で返す。
func loginHandler(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 1. メールアドレスでローカルアカウントを検索する（password_hash は NULL の可能性があるため NullString で受ける）
	var user User
	var status string
	var passwordHash sql.NullString
	err := dbAuth.QueryRow(context.Background(),
		"SELECT id, username, email, password_hash, is_admin, status FROM users WHERE email = $1 AND provider = 'local'", req.Email).Scan(&user.ID, &user.Username, &user.Email, &passwordHash, &user.IsAdmin, &status)
	user.PasswordHash = passwordHash.String
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
			return
		}
		log.Printf("Database error during login: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error during login"})
		return
	}

	// 2. パスワードを照合する（ユーザー不在と同じ "Invalid credentials" を返し、どちらで失敗したかを区別させない）
	if user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// 削除済み・無効なアカウントはログインさせない（パスワード照合後に判定し、存在の有無を漏らさない）
	if !isLoginAllowedStatus(status) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Account is disabled"})
		return
	}

	// 3. JWT を発行して返す
	token, err := generateJWT(user.ID, user.Username.String, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// googleUserInfo は https://www.googleapis.com/oauth2/v2/userinfo のレスポンスのうち、このサービスで使う項目だけを持つ。
// VerifiedEmail はレスポンスに含まれない場合と false を区別するためポインタにしている。
type googleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail *bool  `json:"verified_email"`
	Name          string `json:"name"` // Google アカウントの設定名 (氏名)
}

// setOAuthStateCookie は OAuth の state を保持する HttpOnly Cookie を設定する。
// maxAge に負の値を渡すと Cookie を削除する。
func setOAuthStateCookie(c *gin.Context, value string, maxAge int) {
	// Domain は指定しない（host-only Cookie）。Google からのトップレベル GET で送られるよう SameSite=Lax。
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    value,
		Path:     oauthStateCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// googleCallbackHandler は GET /api/auth/google/callback を処理する。
// state を検証して Google ユーザー情報を取得し、ユーザーを検索または新規作成して JWT を発行する。
// JWT は直接渡さず、Redis に保存したワンタイムコードを付けて /login/success へリダイレクトする。
// エラー時は /login?error=... へリダイレクトする。
func googleCallbackHandler(c *gin.Context) {
	// 1. state Cookie とクエリの state を照合する（CSRF 対策）
	oauthState, _ := c.Cookie(oauthStateCookieName)
	// state は一度きり: 照合の成否にかかわらず Cookie を削除する
	setOAuthStateCookie(c, "", -1)

	queryState := c.Query("state")
	if oauthState == "" || queryState == "" || subtle.ConstantTimeCompare([]byte(queryState), []byte(oauthState)) != 1 {
		log.Println("WARN: OAuth state mismatch or missing")
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=state_mismatch")
		return
	}

	// 2. 認可コードを Google のトークンに交換し、ユーザー情報を取得する
	code := c.Query("code")
	if code == "" {
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=google_error")
		return
	}

	googleUser, err := getGoogleUserData(c.Request.Context(), code)
	if err != nil {
		log.Printf("WARN: Failed to get Google user data: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=google_error")
		return
	}
	log.Printf("DEBUG: Google user data received - ID: %s", googleUser.ID)

	// 3. Google のユーザー ID（provider_id）で既存ユーザーを検索する
	var user User
	var status string
	err = dbAuth.QueryRow(context.Background(),
		"SELECT id, username, is_admin, status FROM users WHERE provider = 'google' AND provider_id = $1", googleUser.ID).Scan(&user.ID, &user.Username, &user.IsAdmin, &status)

	if err == pgx.ErrNoRows {
		// 3a. 未登録なら新規ユーザーを作成する
		log.Printf("DEBUG: User not found in DB, attempting to create new user for Google ID: %s", googleUser.ID)

		// Googleユーザー新規作成時に UUID v7 を採番
		newID, err := uuid.NewV7()
		if err != nil {
			log.Printf("ERROR: Failed to generate UUIDv7 for Google user: %v", err)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=creation_failed")
			return
		}

		// 初期ユーザー名は Google ID の先頭 8 文字から "user_xxxxxxxx" の形で作る
		idPrefix := googleUser.ID
		if len(idPrefix) > 8 {
			idPrefix = idPrefix[:8]
		}
		defaultUsername := fmt.Sprintf("user_%s", idPrefix)
		// google_name カラムに Googleの Name を INSERT
		err = dbAuth.QueryRow(context.Background(),
			"INSERT INTO users (id, email, provider, provider_id, username, google_name, status) VALUES ($1, $2, 'google', $3, $4, $5, 'active') RETURNING id, username, is_admin",
			newID, googleUser.Email, googleUser.ID, defaultUsername, googleUser.Name).Scan(&user.ID, &user.Username, &user.IsAdmin)

		if err != nil {
			log.Printf("ERROR: Failed to create new Google user: %v", err)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=creation_failed")
			return
		}
		log.Printf("DEBUG: New Google user created with ID: %s, Username: %s", user.ID.String(), user.Username.String)

		// profile-service へ初期プロフィールの自動作成リクエストを送信
		// ログインのリダイレクトを待たせないよう非同期で行う。リクエスト終了後も続くため独立したコンテキストを使う
		go func(userIDStr, username string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := createProfile(ctx, userIDStr, username); err != nil {
				log.Printf("ERROR: Failed to create profile for user %s: %v", userIDStr, err)
				return
			}
			log.Printf("DEBUG: Successfully triggered profile creation for user %s", userIDStr)
		}(user.ID.String(), user.Username.String)

	} else if err != nil {
		log.Printf("ERROR: Database error during Google user lookup: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=db_error")
		return
	} else {
		// 3b. 既存ユーザーなら status を確認し、削除済み・無効なアカウントは拒否する
		if !isLoginAllowedStatus(status) {
			log.Printf("WARN: Rejected Google login for disabled account %s (status=%s)", user.ID.String(), status)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=account_disabled")
			return
		}
		log.Printf("DEBUG: Existing Google user found with ID: %s, Username: %s", user.ID.String(), user.Username.String)
		// 既存ユーザーの Google 名が変更されている可能性を考慮し更新
		_, updateErr := dbAuth.Exec(context.Background(),
			"UPDATE users SET google_name = $1 WHERE id = $2", googleUser.Name, user.ID)
		if updateErr != nil {
			log.Printf("WARNING: Failed to update google_name for user %s: %v", user.ID.String(), updateErr)
		}
	}

	// 4. JWT を発行する
	token, err := generateJWT(user.ID, user.Username.String, user.IsAdmin)
	if err != nil {
		log.Printf("ERROR: Failed to generate JWT for user %s: %v", user.ID.String(), err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=jwt_failed")
		return
	}

	// 5. ワンタイムコードを発行してフロントエンドへリダイレクトする
	// JWT を URL に載せず、60 秒・1 回限りのコードを Redis に保存してコードだけを渡す。
	// フロントエンドは POST /api/auth/oauth/exchange でトークンに交換する。
	oneTimeCode, err := randomURLToken(32)
	if err != nil {
		log.Printf("ERROR: Failed to generate one-time code: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=jwt_failed")
		return
	}
	if err := redisClient.Set(c.Request.Context(), oauthCodeKeyPrefix+oneTimeCode, token, oauthCodeTTL).Err(); err != nil {
		log.Printf("ERROR: Failed to store one-time code: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=jwt_failed")
		return
	}
	log.Printf("DEBUG: One-time login code issued for user %s", user.ID.String())

	c.Redirect(http.StatusTemporaryRedirect, "/login/success?code="+oneTimeCode)
}

// oauthExchangeHandler: POST /api/auth/oauth/exchange {"code"} → 200 {"token"}
// コードは GETDEL で取り出すため 1 回しか使えない。
func oauthExchangeHandler(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	// 空や過度に長いコードは Redis に問い合わせる前に弾く
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" || len(req.Code) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	token, err := redisClient.GetDel(c.Request.Context(), oauthCodeKeyPrefix+req.Code).Result()
	if err == redis.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired code"})
		return
	}
	if err != nil {
		log.Printf("Redis error exchanging one-time code: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// googleLoginHandler は GET /api/auth/google/login を処理する。
// ランダムな state を生成して Cookie に保存し、Google の認可画面へリダイレクトする。
func googleLoginHandler(c *gin.Context) {
	state, err := randomURLToken(32)
	if err != nil {
		log.Printf("ERROR: Failed to generate OAuth state: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=google_error")
		return
	}
	setOAuthStateCookie(c, state, oauthStateMaxAge)
	c.Redirect(http.StatusTemporaryRedirect, googleOauthConfig.AuthCodeURL(state))
}

// callInternal は X-Internal-Key ヘッダーを付けて他の内部サービスへリクエストを送る。
// 2xx 以外のレスポンスはエラーとして返す（レスポンス本文の先頭 1KB をエラーメッセージに含める）。
func callInternal(ctx context.Context, method, url string, body []byte) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if internalAPIKey != "" {
		req.Header.Set("X-Internal-Key", internalAPIKey)
	}
	resp, err := internalHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s returned status %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// createProfile は profile-service に初期プロフィール行の作成を依頼する（リクエストボディの形式は従来どおり）。
func createProfile(ctx context.Context, userID, username string) error {
	payload, _ := json.Marshal(map[string]string{
		"user_id":  userID,
		"username": username,
	})
	return callInternal(ctx, http.MethodPost, profileServiceURL+"/internal/create", payload)
}

// removeObjectsWithPrefix は bucket 内で prefix から始まるすべてのオブジェクトを削除する。
// 途中で失敗しても残りの削除は続け、最初に発生したエラーだけを返す。
func removeObjectsWithPrefix(ctx context.Context, client *minio.Client, bucket, prefix string) error {
	var firstErr error
	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			if firstErr == nil {
				firstErr = object.Err
			}
			continue
		}
		if err := client.RemoveObject(ctx, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// queryIDs は app-db に query（引数 args）を実行し、結果の id 列を文字列のスライスで返す。
func queryIDs(ctx context.Context, query string, args ...interface{}) ([]string, error) {
	rows, err := dbApp.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deleteAccountHandler は DELETE /api/auth/me を処理し、ログイン中ユーザーのアカウントと関連データを削除する。
// profile-service / team-service のデータ、MinIO 上のファイル、app-db の行を順に削除し、
// 最後に auth-db のユーザーを処理する（ローカルは行を削除、Google は status を 'deleted_data' にして個人情報を消す）。
// 途中の後始末が失敗しても処理は続け、失敗した項目名を cleanupErrors としてレスポンスに含める。
func deleteAccountHandler(c *gin.Context) {
	userIDStr := c.GetString("userID")
	if userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user session"})
		return
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID format"})
		return
	}

	// クライアントが切断しても後始末を途中で止めないよう、リクエストとは独立したコンテキストを使う
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 最後の auth-db の処理方法を決めるため provider（local / google）を取得する
	var provider string
	err = dbAuth.QueryRow(ctx, "SELECT provider FROM users WHERE id = $1", userID).Scan(&provider)
	if err != nil {
		log.Printf("Error getting user provider for deletion: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// 失敗した後始末の項目（レスポンスの cleanupErrors に入れる。詳細はログのみ）
	cleanupErrors := []string{}
	fail := func(step string, err error) {
		log.Printf("Warning: account deletion step %q failed for user %s: %v", step, userID, err)
		cleanupErrors = append(cleanupErrors, step)
	}

	// --- profile-db (profile-service) ---
	if err := callInternal(ctx, http.MethodDelete, fmt.Sprintf("%s/internal/users/%s", profileServiceURL, userID), nil); err != nil {
		fail("profile-service", err)
	}

	// --- team-db (team-service) ---
	if err := callInternal(ctx, http.MethodDelete, fmt.Sprintf("%s/internal/users/%s", teamServiceURL, userID), nil); err != nil {
		fail("team-service", err)
	}

	// --- MinIO 上のプロフィール画像（キー: icons/<userId>-<uuid>.<ext>, backgrounds/<userId>-<uuid>.<ext>） ---
	// 末尾に "-" を付け、ID が前方一致する別ユーザーのファイルを消さないようにしている
	for _, prefix := range []string{"icons/" + userID.String() + "-", "backgrounds/" + userID.String() + "-"} {
		if err := removeObjectsWithPrefix(ctx, minioClientProfile, profileBucketName, prefix); err != nil {
			fail("profile-images", err)
			break
		}
	}

	// --- ゲーム: MinIO の games/<id>/ と thumbnails/<id> を消してから app-db の行を削除する ---
	gameIDs, err := queryIDs(ctx, "SELECT id::text FROM games WHERE user_id = $1", userID)
	if err != nil {
		fail("games", err)
	}
	for _, gameID := range gameIDs {
		if err := removeObjectsWithPrefix(ctx, minioClientGame, gameBucketName, gameID+"/"); err != nil {
			fail("game:"+gameID, err)
		}
		if err := minioClientGame.RemoveObject(ctx, gameBucketName, "thumbnails/"+gameID, minio.RemoveObjectOptions{}); err != nil {
			fail("game-thumbnail:"+gameID, err)
		}
		if _, err := dbApp.Exec(ctx, "DELETE FROM games WHERE id = $1", gameID); err != nil {
			fail("game-record:"+gameID, err)
		}
	}

	// --- 静的サイト: MinIO の static-sites/<id>/ とサムネイル（thumbnail_url から求める）を消してから app-db の行を削除する ---
	// siteRow は削除対象の静的サイトの ID とサムネイル URL を保持する。
	type siteRow struct {
		id        string
		thumbnail sql.NullString
	}
	sites := []siteRow{}
	if rows, err := dbApp.Query(ctx, "SELECT id::text, thumbnail_url FROM static_sites WHERE user_id = $1", userID); err != nil {
		fail("static-sites", err)
	} else {
		for rows.Next() {
			var s siteRow
			if err := rows.Scan(&s.id, &s.thumbnail); err != nil {
				fail("static-sites", err)
				break
			}
			sites = append(sites, s)
		}
		rows.Close()
	}
	for _, site := range sites {
		if err := removeObjectsWithPrefix(ctx, minioClientStaticSite, staticSiteBucketName, site.id+"/"); err != nil {
			fail("static-site:"+site.id, err)
		}
		// "/static-sites/thumbnails/xxxx.ext" → "thumbnails/xxxx.ext"
		// DB の値をそのままキーにするため、thumbnails/ 配下で ".." を含まないものだけを削除対象にする
		if site.thumbnail.Valid {
			if obj, ok := strings.CutPrefix(site.thumbnail.String, "/static-sites/"); ok && strings.HasPrefix(obj, "thumbnails/") && !strings.Contains(obj, "..") {
				if err := minioClientStaticSite.RemoveObject(ctx, staticSiteBucketName, obj, minio.RemoveObjectOptions{}); err != nil {
					fail("static-site-thumbnail:"+site.id, err)
				}
			}
		}
		if _, err := dbApp.Exec(ctx, "DELETE FROM static_sites WHERE id = $1", site.id); err != nil {
			fail("static-site-record:"+site.id, err)
		}
	}

	// --- 動画: MinIO の videos/<id>/（HLS）と thumbnails/<id> を消してから app-db の行を削除する ---
	// 動画用 MinIO クライアントが未設定の場合はファイル削除を失敗扱いにし、DB の行だけ削除する
	videoIDs, err := queryIDs(ctx, "SELECT id::text FROM videos WHERE uploader_id = $1", userID)
	if err != nil {
		fail("videos", err)
	}
	for _, videoID := range videoIDs {
		if minioClientVideo == nil {
			fail("video:"+videoID, errors.New("video MinIO credentials are not configured"))
		} else {
			if err := removeObjectsWithPrefix(ctx, minioClientVideo, videoBucketName, videoID+"/"); err != nil {
				fail("video:"+videoID, err)
			}
			if err := minioClientVideo.RemoveObject(ctx, videoBucketName, "thumbnails/"+videoID, minio.RemoveObjectOptions{}); err != nil {
				fail("video-thumbnail:"+videoID, err)
			}
		}
		if _, err := dbApp.Exec(ctx, "DELETE FROM videos WHERE id = $1", videoID); err != nil {
			fail("video-record:"+videoID, err)
		}
	}

	// --- 最後に auth-db のユーザーレコードを処理する ---
	// ローカルアカウントは行ごと削除する。Google アカウントは行を残して status を 'deleted_data' にし、
	// ユーザー名と Google 名を消す（以後の Google ログインは isLoginAllowedStatus で拒否される）
	if provider == "local" {
		_, err = dbAuth.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
		if err != nil {
			log.Printf("Error deleting local user account from auth-db: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "アカウントの削除に失敗しました。", "cleanupErrors": cleanupErrors})
			return
		}
	} else {
		_, err = dbAuth.Exec(ctx, "UPDATE users SET status = 'deleted_data', username = NULL, google_name = NULL WHERE id = $1", userID)
		if err != nil {
			log.Printf("Error updating Google user status after data deletion: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "アカウントデータの削除に失敗しました。", "cleanupErrors": cleanupErrors})
			return
		}
	}

	// 使用中のトークンも無効化する（他サービスもブロックリストを参照する）
	if tokenString := c.GetString("tokenString"); tokenString != "" {
		if token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{}); err == nil {
			if claims, ok := token.Claims.(jwt.MapClaims); ok {
				jti, _ := claims["jti"].(string)
				exp, _ := claims["exp"].(float64)
				if ttl := time.Until(time.Unix(int64(exp), 0)); jti != "" && ttl > 0 {
					if err := redisClient.Set(ctx, fmt.Sprintf("blocklist:%s", jti), true, ttl).Err(); err != nil {
						log.Printf("Warning: Failed to blocklist token after account deletion: %v", err)
					}
				}
			}
		}
	}

	if len(cleanupErrors) > 0 {
		c.JSON(http.StatusOK, gin.H{
			"message":       "アカウントは削除されましたが、一部のデータの削除に失敗しました。",
			"cleanupErrors": cleanupErrors,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "アカウントデータが正常に削除されました。", "cleanupErrors": cleanupErrors})
}

// getMeHandler は GET /api/auth/me を処理し、JWT のユーザーの auth-db 上のアカウント情報を返す
// （provider はフロントエンドのアカウント削除ダイアログなどで必要になる）。
func getMeHandler(c *gin.Context) {
	userID, ok := c.Get("userUUID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user session"})
		return
	}

	var (
		email    string
		username sql.NullString
		provider string
		isAdmin  bool
		status   string
	)
	err := dbAuth.QueryRow(c.Request.Context(),
		"SELECT email, username, provider, COALESCE(is_admin, FALSE), status FROM users WHERE id = $1", userID).
		Scan(&email, &username, &provider, &isAdmin, &status)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if err != nil {
		log.Printf("Error getting auth user info: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user info"})
		return
	}

	// username が NULL の場合は JSON で null を返す
	var usernameValue interface{}
	if username.Valid {
		usernameValue = username.String
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       userID.(uuid.UUID).String(),
		"email":    email,
		"username": usernameValue,
		"provider": provider,
		"isAdmin":  isAdmin,
		"status":   status,
	})
}

// generateJWT は HS256 で署名した有効期限 7 日の JWT を生成する。
// ログアウト時のブロックリスト登録に使うため、jti にランダムな UUID を設定する。
// JWT 生成処理で userID (uuid.UUID) を文字列としてセット
func generateJWT(userID uuid.UUID, username string, isAdmin bool) (string, error) {
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("failed to generate jti: %w", err)
	}

	userIDStr := userID.String()

	// トークンを読む側の互換性のため、同じユーザー ID を sub / userID / user_id / id に重複して入れている
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      userIDStr,
		"userID":   userIDStr,
		"user_id":  userIDStr,
		"id":       userIDStr,
		"username": username,
		"isAdmin":  isAdmin,
		"exp":      time.Now().Add(time.Hour * 24 * 7).Unix(),
		"jti":      jti.String(),
	})
	return token.SignedString(jwtSecret)
}

// getGoogleUserData は認可コードをアクセストークンに交換し、Google の userinfo API からユーザー情報を取得する。
// ID・メールアドレスが空の場合や、メールアドレスが未確認（verified_email が false）の場合はエラーを返す。
func getGoogleUserData(parent context.Context, code string) (*googleUserInfo, error) {
	// 1. タイムアウト付きのコンテキストを作り、oauth2 ライブラリにもタイムアウト付き HTTP クライアントを使わせる
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, internalHTTPClient)

	// 2. 認可コードをトークンに交換する
	token, err := googleOauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}

	// 3. userinfo API を呼び出す
	// アクセストークンは URL ではなく Authorization ヘッダーで送る
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := internalHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed getting user info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %d", response.StatusCode)
	}

	// 4. レスポンスを読み込んで検証する（読み込みは最大 1MiB に制限する）
	// verified_email がレスポンスにない場合は拒否せず通す
	var info googleUserInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode user info: %w", err)
	}
	info.ID = strings.TrimSpace(info.ID)
	info.Email = strings.TrimSpace(info.Email)
	if info.ID == "" || info.Email == "" {
		return nil, errors.New("user info is missing id or email")
	}
	if info.VerifiedEmail != nil && !*info.VerifiedEmail {
		return nil, errors.New("google email is not verified")
	}
	return &info, nil
}
