package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	jwtSecret             []byte
	googleOauthConfig     *oauth2.Config
	appURL                string
	adminRegistrationCode string
	profileBucketName     string = "user-profiles"
	gameBucketName        string = "games"
	staticSiteBucketName  string = "static-sites"
	videoStoragePath      string
	thumbnailDir          string
)

const oauthStateCookieName = "oauthstate"
const adminEmail = "admin@internal.local"

// User 構造体（ID を uuid.UUID に設定）
type User struct {
	ID           uuid.UUID      `json:"id"`
	Username     sql.NullString `json:"username"`
	GoogleName   sql.NullString `json:"google_name"`
	Email        string         `json:"email"`
	PasswordHash string         `json:"-"`
	Provider     string         `json:"provider"`
	ProviderID   sql.NullString `json:"-"`
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

		// Check if the token is in the blocklist (Fail-Closed)
		exists, err := redisClient.Exists(c.Request.Context(), fmt.Sprintf("blocklist:%s", tokenString)).Result()
		if err != nil {
			log.Printf("Redis error checking blocklist: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
			return
		}
		if exists > 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been invalidated"})
			return
		}

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
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
	thumbnailDir = os.Getenv("THUMBNAIL_DIR")

	// Initialize Redis client
	for i := 0; i < 5; i++ {
		redisClient = redis.NewClient(&redis.Options{Addr: redisAddr})
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
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	videoStoragePath = os.Getenv("VIDEO_STORAGE_PATH")

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
		authRoutes.DELETE("/me", authMiddleware(), deleteAccountHandler)
		authRoutes.GET("/user/:userId", getUserProviderHandler)
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
	if adminRegistrationCode == "" || req.AdminCode != adminRegistrationCode {
		log.Printf("DEBUG: Restricted registration attempt. Code received: '%s'", req.AdminCode)
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid Admin Code."})
		return
	}

	// 既に管理者（adminEmail: "admin@internal.local"）が存在するかチェック
	var existingAdminID uuid.UUID
	err := dbAuth.QueryRow(context.Background(), "SELECT id FROM users WHERE email = $1 AND provider = 'local'", adminEmail).Scan(&existingAdminID)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"message": "Admin account already exists.", "userID": existingAdminID.String()})
		return
	}
	if err != pgx.ErrNoRows {
		log.Printf("Database error while checking for admin: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error while checking for admin."})
		return
	}

	// メールアドレス・パスワードが送られてこない場合はデフォルト値を補填
	email := req.Email
	if email == "" {
		email = adminEmail
	}

	password := req.Password
	if password == "" {
		password = "AdminPassword123!"
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

	username := "admin"

	var userID uuid.UUID
	err = dbAuth.QueryRow(context.Background(),
		"INSERT INTO users (id, username, email, password_hash, provider, is_admin, status) VALUES ($1, $2, $3, $4, 'local', $5, 'active') RETURNING id",
		newID, username, email, string(hashedPassword), true).Scan(&userID)
	if err != nil {
		log.Printf("Failed to create admin user: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create admin user. Email might already be in use."})
		return
	}

	// profile-service へ管理者の初期プロフィール作成を非同期リクエスト
	go func(userIDStr, uname string) {
		profileServiceURL := os.Getenv("PROFILE_SERVICE_URL")
		if profileServiceURL == "" {
			profileServiceURL = "http://profile-service:8083"
		}

		payload, _ := json.Marshal(map[string]string{
			"user_id":  userIDStr,
			"username": uname,
		})

		resp, err := http.Post(
			fmt.Sprintf("%s/api/profile/internal/create", profileServiceURL),
			"application/json",
			bytes.NewBuffer(payload),
		)
		if err != nil {
			log.Printf("ERROR: Failed to call profile-service creation endpoint: %v", err)
			return
		}
		defer resp.Body.Close()
		log.Printf("DEBUG: Profile creation triggered for admin %s", userIDStr)
	}(userID.String(), username)

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
	err := dbAuth.QueryRow(context.Background(),
		"SELECT id, username, email, password_hash, is_admin FROM users WHERE email = $1 AND provider = 'local'", req.Email).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.IsAdmin)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
			return
		}
		log.Printf("Database error during login: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error during login"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	token, err := generateJWT(user.ID, user.Username.String, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

func googleCallbackHandler(c *gin.Context) {
	oauthState, _ := c.Cookie(oauthStateCookieName)
	if c.Query("state") != oauthState {
		log.Println("DEBUG: OAuth state mismatch")
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=state_mismatch")
		return
	}

	data, err := getGoogleUserData(c.Query("code"))
	if err != nil {
		log.Printf("DEBUG: Failed to get Google user data: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=google_error")
		return
	}

	var googleUser struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"` // Google アカウントの設定名 (氏名)
	}
	if err := json.Unmarshal(data, &googleUser); err != nil {
		log.Printf("DEBUG: Failed to unmarshal Google user data: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=json_error")
		return
	}
	log.Printf("DEBUG: Google user data - ID: %s, Email: %s, Name: %s", googleUser.ID, googleUser.Email, googleUser.Name)

	var user User
	err = dbAuth.QueryRow(context.Background(),
		"SELECT id, username, is_admin FROM users WHERE provider = 'google' AND provider_id = $1", googleUser.ID).Scan(&user.ID, &user.Username, &user.IsAdmin)

	if err == pgx.ErrNoRows {
		log.Printf("DEBUG: User not found in DB, attempting to create new user for Google ID: %s", googleUser.ID)

		// Googleユーザー新規作成時に UUID v7 を採番
		newID, err := uuid.NewV7()
		if err != nil {
			log.Printf("ERROR: Failed to generate UUIDv7 for Google user: %v", err)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=creation_failed")
			return
		}

		defaultUsername := fmt.Sprintf("user_%s", googleUser.ID[:8])
		// google_name カラムに Googleの Name を INSERT
		err = dbAuth.QueryRow(context.Background(),
			"INSERT INTO users (id, email, provider, provider_id, username, google_name, status) VALUES ($1, $2, 'google', $3, $4, $5, 'active') RETURNING id, username, is_admin",
			newID, googleUser.Email, googleUser.ID, defaultUsername, googleUser.Name).Scan(&user.ID, &user.Username, &user.IsAdmin)

		if err != nil {
			log.Printf("ERROR: Failed to create new Google user: %v", err)
			c.Redirect(http.StatusTemporaryRedirect, "/login?error=creation_failed")
			return
		}
		log.Printf("DEBUG: New Google user created with ID: %s, Username: %s, GoogleName: %s", user.ID.String(), user.Username.String, googleUser.Name)

		// profile-service へ初期プロフィールの自動作成リクエストを送信
		go func(userIDStr, username string) {
			profileServiceURL := os.Getenv("PROFILE_SERVICE_URL")
			if profileServiceURL == "" {
				profileServiceURL = "http://profile-service:8083" // Docker Compose内のデフォルト
			}

			payload, _ := json.Marshal(map[string]string{
				"user_id":  userIDStr,
				"username": username,
			})

			resp, err := http.Post(
				fmt.Sprintf("%s/api/profile/internal/create", profileServiceURL),
				"application/json",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				log.Printf("ERROR: Failed to call profile-service creation endpoint: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				log.Printf("WARNING: profile-service returned status code: %d", resp.StatusCode)
			} else {
				log.Printf("DEBUG: Successfully triggered profile creation for user %s", userIDStr)
			}
		}(user.ID.String(), user.Username.String)

	} else if err != nil {
		log.Printf("ERROR: Database error during Google user lookup: %v", err)
		c.Redirect(http.StatusTemporaryRedirect, "/login?error=db_error")
		return
	} else {
		log.Printf("DEBUG: Existing Google user found with ID: %s, Username: %s", user.ID.String(), user.Username.String)
		// 既存ユーザーの Google 名が変更されている可能性を考慮し更新
		_, updateErr := dbAuth.Exec(context.Background(),
			"UPDATE users SET google_name = $1, updated_at = NOW() WHERE id = $2", googleUser.Name, user.ID)
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
	log.Printf("DEBUG: JWT generated for user %s", user.ID.String())

	// クエリパラメータで Google から取得した名前を返却・開示する（URLエンコード付き）
	redirectURL := fmt.Sprintf("/login/success?token=%s&google_name=%s", token, url.QueryEscape(googleUser.Name))
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

func googleLoginHandler(c *gin.Context) {
	state := uuid.New().String()
	c.SetCookie(oauthStateCookieName, state, 3600, "/", "localhost", false, true)
	url := googleOauthConfig.AuthCodeURL(state)
	c.Redirect(http.StatusTemporaryRedirect, url)
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

	var provider string
	var iconURL, backgroundURL sql.NullString
	err = dbAuth.QueryRow(context.Background(), "SELECT provider, icon_url, background_image_url FROM users WHERE id = $1", userID).Scan(&provider, &iconURL, &backgroundURL)
	if err != nil {
		log.Printf("Error getting user provider/profile for deletion: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// --- Delete profile assets from MinIO (profile-storage) ---
	if iconURL.Valid && iconURL.String != "" {
		objectName := strings.TrimPrefix(iconURL.String, "/user-profiles/")
		if err := minioClientProfile.RemoveObject(context.Background(), profileBucketName, objectName, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("Warning: Failed to delete icon %s from MinIO: %v", objectName, err)
		}
	}
	if backgroundURL.Valid && backgroundURL.String != "" {
		objectName := strings.TrimPrefix(backgroundURL.String, "/user-profiles/")
		if err := minioClientProfile.RemoveObject(context.Background(), profileBucketName, objectName, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("Warning: Failed to delete background %s from MinIO: %v", objectName, err)
		}
	}

	// --- Delete game assets and records from MinIO (game-storage) and app-db ---
	gameIDs := []string{}
	rows, err := dbApp.Query(context.Background(), "SELECT id FROM games WHERE user_id = $1", userID)
	if err != nil {
		log.Printf("Warning: Failed to get game IDs for user %s: %v", userID, err)
	} else {
		for rows.Next() {
			var gameID string
			if err := rows.Scan(&gameID); err != nil {
				log.Printf("Warning: Failed to scan game ID: %v", err)
				continue
			}
			gameIDs = append(gameIDs, gameID)
		}
		rows.Close()
	}

	for _, gameID := range gameIDs {
		objectPrefix := fmt.Sprintf("%s/", gameID)
		objectsCh := minioClientGame.ListObjects(context.Background(), gameBucketName, minio.ListObjectsOptions{
			Prefix:    objectPrefix,
			Recursive: true,
		})
		for object := range objectsCh {
			if object.Err != nil {
				log.Printf("Warning: Error listing objects for game %s: %v", gameID, object.Err)
				continue
			}
			if err := minioClientGame.RemoveObject(context.Background(), gameBucketName, object.Key, minio.RemoveObjectOptions{}); err != nil {
				log.Printf("Warning: Failed to delete game object %s from MinIO: %v", object.Key, err)
			}
		}
		if _, err := dbApp.Exec(context.Background(), "DELETE FROM games WHERE id = $1", gameID); err != nil {
			log.Printf("Warning: Failed to delete game record %s from app-db: %v", gameID, err)
		}
	}

	// --- Delete static site assets and records from MinIO (static-site-storage) and app-db ---
	staticSiteIDs := []string{}
	rows, err = dbApp.Query(context.Background(), "SELECT id FROM static_sites WHERE user_id = $1", userID)
	if err != nil {
		log.Printf("Warning: Failed to get static site IDs for user %s: %v", userID, err)
	} else {
		for rows.Next() {
			var siteID string
			if err := rows.Scan(&siteID); err != nil {
				log.Printf("Warning: Failed to scan static site ID: %v", err)
				continue
			}
			staticSiteIDs = append(staticSiteIDs, siteID)
		}
		rows.Close()
	}

	for _, siteID := range staticSiteIDs {
		objectPrefix := fmt.Sprintf("%s/", siteID)
		objectsCh := minioClientStaticSite.ListObjects(context.Background(), staticSiteBucketName, minio.ListObjectsOptions{
			Prefix:    objectPrefix,
			Recursive: true,
		})
		for object := range objectsCh {
			if object.Err != nil {
				log.Printf("Warning: Error listing objects for static site %s: %v", siteID, object.Err)
				continue
			}
			if err := minioClientStaticSite.RemoveObject(context.Background(), staticSiteBucketName, object.Key, minio.RemoveObjectOptions{}); err != nil {
				log.Printf("Warning: Failed to delete static site object %s from MinIO: %v", object.Key, err)
			}
		}
		if _, err := dbApp.Exec(context.Background(), "DELETE FROM static_sites WHERE id = $1", siteID); err != nil {
			log.Printf("Warning: Failed to delete static site record %s from app-db: %v", siteID, err)
		}
	}

	// --- Delete video assets and records from local storage and app-db ---
	videoIDs := []string{}
	rows, err = dbApp.Query(context.Background(), "SELECT id, filename FROM videos WHERE uploader_id = $1", userID)
	if err != nil {
		log.Printf("Warning: Failed to get video IDs for user %s: %v", userID, err)
	} else {
		for rows.Next() {
			var videoID, filename string
			if err := rows.Scan(&videoID, &filename); err != nil {
				log.Printf("Warning: Failed to scan video ID/filename: %v", err)
				continue
			}
			hlsPath := filepath.Join(videoStoragePath, videoID)
			if err := os.RemoveAll(hlsPath); err != nil {
				log.Printf("Warning: Failed to delete HLS directory %s: %v", hlsPath, err)
			}
			thumbnailFilename := fmt.Sprintf("%s.jpg", videoID)
			thumbnailPath := filepath.Join(thumbnailDir, thumbnailFilename)
			if err := os.Remove(thumbnailPath); err != nil {
				log.Printf("Warning: Failed to delete thumbnail file %s: %v", thumbnailPath, err)
			}
			videoIDs = append(videoIDs, videoID)
		}
		rows.Close()
	}

	for _, videoID := range videoIDs {
		if _, err := dbApp.Exec(context.Background(), "DELETE FROM videos WHERE id = $1", videoID); err != nil {
			log.Printf("Warning: Failed to delete video record %s from app-db: %v", videoID, err)
		}
	}

	// --- Finally, delete user record from auth-db ---
	if provider == "local" {
		_, err = dbAuth.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
		if err != nil {
			log.Printf("Error deleting local user account from auth-db: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "アカウントの削除に失敗しました。"})
			return
		}
	} else {
		_, err = dbAuth.Exec(context.Background(), "UPDATE users SET status = 'deleted_data', username = NULL, google_name = NULL, icon_url = NULL, background_image_url = NULL, bio = NULL WHERE id = $1", userID)
		if err != nil {
			log.Printf("Error updating Google user status after data deletion: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "アカウントデータの削除に失敗しました。"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "アカウントデータが正常に削除されました。"})
}

func getUserProviderHandler(c *gin.Context) {
	// URLパラメータからの UUID パース
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID format"})
		return
	}

	var provider string
	err = dbAuth.QueryRow(context.Background(), "SELECT provider FROM users WHERE id = $1", userID).Scan(&provider)
	if err != nil {
		log.Printf("Error getting user provider: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"provider": provider})
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

func getGoogleUserData(code string) ([]byte, error) {
	token, err := googleOauthConfig.Exchange(context.Background(), code)
	if err != nil {
		return nil, fmt.Errorf("code exchange wrong: %s", err.Error())
	}
	response, err := http.Get("https://www.googleapis.com/oauth2/v2/userinfo?access_token=" + token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed getting user info: %s", err.Error())
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed read user info: %s", err.Error())
	}
	return contents, nil
}