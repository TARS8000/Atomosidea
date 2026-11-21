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

var (
	dbAuth                *pgxpool.Pool
	dbApp                 *pgxpool.Pool
	redisClient           *redis.Client
	minioClientProfile    *minio.Client
	minioClientGame       *minio.Client
	minioClientStaticSite *minio.Client
	minioClientVideo      *minio.Client // nil when VIDEO_MINIO_* credentials are not configured
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

	// internalHTTPClient is used for service-to-service calls (profile-service, team-service, Google).
	internalHTTPClient = &http.Client{Timeout: 15 * time.Second}
)

const oauthStateCookieName = "oauthstate"
const oauthStateCookiePath = "/api/auth/google"
const oauthStateMaxAge = 10 * 60 // seconds
const oauthCodeKeyPrefix = "oauth_code:"
const oauthCodeTTL = 60 * time.Second
const minAdminPasswordLength = 8

// errAccountDisabled is returned when the user exists but its status does not allow login.
var errAccountDisabled = errors.New("account is disabled")

// isLoginAllowedStatus reports whether a users.status value may log in / use a token.
// Values in auth-db: 'active' / 'pending' / 'deleted_data'. Only 'deleted_data' (and any
// unknown value) is rejected; 'pending' is kept for rows created by older versions.
func isLoginAllowedStatus(status string) bool {
	return status == "active" || status == "pending"
}

// randomURLToken returns n random bytes encoded as base64url (no padding).
func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// User 構造体（ID を uuid.UUID に設定）
type User struct {
	ID           uuid.UUID      `json:"id"`
	Username     sql.NullString `json:"username"`
	Email        string         `json:"email"`
	PasswordHash string         `json:"-"`
	IsAdmin      bool           `json:"is_admin"`
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			// Check if the token is in the blocklist (Fail-Closed)
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

			// JWTからのID取得を文字列（UUID形式）として処理
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

			// Reject tokens of deleted / disabled accounts (fail-closed on DB errors)
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

func main() {
	var err error
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

	// Initialize Redis client
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

	// MinIO credentials
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

	// Initialize auth-db
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

	// Initialize app-db
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

	// Initialize MinIO client for profile assets
	minioClientProfile, err = minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(profileMinioAccessKey, profileMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to profile MinIO: %v", err)
	}

	// Initialize MinIO client for game assets
	minioClientGame, err = minio.New(gameMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(gameMinioAccessKey, gameMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to game MinIO: %v", err)
	}

	// Initialize MinIO client for static site assets
	minioClientStaticSite, err = minio.New(staticSiteMinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(staticSiteMinioAccessKey, staticSiteMinioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		log.Fatalf("Unable to connect to static site MinIO: %v", err)
	}

	// Initialize MinIO client for video assets (HLS + thumbnails), used on account deletion
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

	r := gin.Default()
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{appURL}
	config.AllowMethods = []string{"GET", "POST", "OPTIONS", "DELETE"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	r.Use(cors.New(config))

	authRoutes := r.Group("/api/auth")
	{
		authRoutes.POST("/register", registerHandler)
		authRoutes.POST("/login", loginHandler)
		authRoutes.POST("/logout", authMiddleware(), logoutHandler)
		authRoutes.POST("/oauth/exchange", oauthExchangeHandler)

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

	log.Println("INFO: Auth service starting on port 8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

func logoutHandler(c *gin.Context) {
	tokenString, exists := c.Get("tokenString")
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token not found in context"})
		return
	}

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

func loginHandler(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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

	if user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// 削除済み・無効なアカウントはログインさせない（パスワード照合後に判定し、存在の有無を漏らさない）
	if !isLoginAllowedStatus(status) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Account is disabled"})
		return
	}

	token, err := generateJWT(user.ID, user.Username.String, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// googleUserInfo is the subset of https://www.googleapis.com/oauth2/v2/userinfo we use.
type googleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail *bool  `json:"verified_email"`
	Name          string `json:"name"` // Google アカウントの設定名 (氏名)
}

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

func googleCallbackHandler(c *gin.Context) {
	oauthState, _ := c.Cookie(oauthStateCookieName)
	// state は一度きり: 照合の成否にかかわらず Cookie を削除する
	setOAuthStateCookie(c, "", -1)

	queryState := c.Query("state")
	if oauthState == "" || queryState == "" || subtle.ConstantTimeCompare([]byte(queryState), []byte(oauthState)) != 1 {
		log.Println("WARN: OAuth state mismatch or missing")
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=state_mismatch")
		return
	}

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

	var user User
	var status string
	err = dbAuth.QueryRow(context.Background(),
		"SELECT id, username, is_admin, status FROM users WHERE provider = 'google' AND provider_id = $1", googleUser.ID).Scan(&user.ID, &user.Username, &user.IsAdmin, &status)

	if err == pgx.ErrNoRows {
		log.Printf("DEBUG: User not found in DB, attempting to create new user for Google ID: %s", googleUser.ID)

		// Googleユーザー新規作成時に UUID v7 を採番
		newID, err := uuid.NewV7()
		if err != nil {
			log.Printf("ERROR: Failed to generate UUIDv7 for Google user: %v", err)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=creation_failed")
			return
		}

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

	token, err := generateJWT(user.ID, user.Username.String, user.IsAdmin)
	if err != nil {
		log.Printf("ERROR: Failed to generate JWT for user %s: %v", user.ID.String(), err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=jwt_failed")
		return
	}

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

// callInternal sends a request to another internal service with X-Internal-Key.
// Non-2xx responses are returned as errors.
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

// createProfile asks profile-service to create the initial profile row (body format unchanged).
func createProfile(ctx context.Context, userID, username string) error {
	payload, _ := json.Marshal(map[string]string{
		"user_id":  userID,
		"username": username,
	})
	return callInternal(ctx, http.MethodPost, profileServiceURL+"/internal/create", payload)
}

// removeObjectsWithPrefix deletes every object under prefix in bucket.
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

// queryIDs returns the id column of rows matching query (args...).
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

	// --- Profile images in MinIO (keys: icons/<userId>-<uuid>.<ext>, backgrounds/<userId>-<uuid>.<ext>) ---
	for _, prefix := range []string{"icons/" + userID.String() + "-", "backgrounds/" + userID.String() + "-"} {
		if err := removeObjectsWithPrefix(ctx, minioClientProfile, profileBucketName, prefix); err != nil {
			fail("profile-images", err)
			break
		}
	}

	// --- Games: MinIO games/<id>/ + thumbnails/<id>, then app-db rows ---
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

	// --- Static sites: MinIO static-sites/<id>/ + thumbnail (from thumbnail_url), then app-db rows ---
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

	// --- Videos: MinIO videos/<id>/ (HLS) + thumbnails/<id>, then app-db rows ---
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

	// --- Finally, delete user record from auth-db ---
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

// getMeHandler returns the auth-db account information of the JWT user
// (provider is needed by the frontend, e.g. for the account deletion dialog).
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

// JWT 生成処理で userID (uuid.UUID) を文字列としてセット
func generateJWT(userID uuid.UUID, username string, isAdmin bool) (string, error) {
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("failed to generate jti: %w", err)
	}

	userIDStr := userID.String()

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

func getGoogleUserData(parent context.Context, code string) (*googleUserInfo, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, internalHTTPClient)

	token, err := googleOauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}

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
