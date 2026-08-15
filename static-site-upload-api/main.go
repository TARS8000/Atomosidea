package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db          *sql.DB
	minioClient *minio.Client
	jwtSecret   string
	minioBucket string
	sfspApiUrl  string
	logger      *zap.SugaredLogger
)

// StaticSite represents a static site entry in the database.
type StaticSite struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	MinioPath      string    `json:"minio_path"`
	EntryPointPath string    `json:"entry_point_path"`
	ThumbnailURL   string    `json:"thumbnail_url"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"` // Add UpdatedAt field
}

type SfspUploadResponse struct {
	FileID   string `json:"file_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
}

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	err = godotenv.Load()
	if err != nil {
		logger.Warnf("Error loading .env file, assuming production environment: %v", err)
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

	buckets := []string{minioBucket}

	for _, bucket := range buckets {
		if bucket == "" {
			continue
		}
		exists, err := minioClient.BucketExists(context.Background(), bucket)
		if err != nil {
			logger.Fatalf("Failed to check MinIO bucket '%s': %v", bucket, err)
		}
		if !exists {
			err = minioClient.MakeBucket(context.Background(), bucket, minio.MakeBucketOptions{})
			if err != nil {
				logger.Fatalf("Failed to create MinIO bucket '%s': %v", bucket, err)
			}
			logger.Infof("Successfully created MinIO bucket: %s", bucket)
		}

		policy := fmt.Sprintf(`{
          "Version": "2012-10-17",
          "Statement": [{
             "Effect": "Allow",
             "Principal": {"AWS": ["*"]},
             "Action": ["s3:GetObject"],
             "Resource": ["arn:aws:s3:::%s/*"]
          }]
       }`, bucket)
		err = minioClient.SetBucketPolicy(context.Background(), bucket, policy)
		if err != nil {
			logger.Warnf("Warning: Failed to set bucket policy to public: %v", err)
		} else {
			logger.Info("Successfully set MinIO bucket policy to Public Read")
		}
	}

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

	r := gin.Default()

	r.GET("/api/static-sites", listStaticSitesHandler)
	r.GET("/api/static-sites/:id", getStaticSiteHandler)

	auth := r.Group("/")
	auth.Use(authMiddleware)
	{
		auth.POST("/api/static-sites/upload", uploadStaticSiteHandler)
		auth.PUT("/api/static-sites/:id", updateStaticSiteHandler) // Add this line
		auth.DELETE("/api/static-sites/:id", deleteStaticSiteHandler)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	logger.Infof("Static Site Upload API running on port %s", port)
	logger.Fatal(r.Run(":" + port))
}

// authMiddleware authenticates requests using JWT.
func authMiddleware(c *gin.Context) {
	tokenString := c.GetHeader("Authorization")
	if tokenString == "" || !strings.HasPrefix(tokenString, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
		c.Abort()
		return
	}
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		c.Abort()
		return
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		var userIDStr string
		if idStr, ok := claims["sub"].(string); ok {
			userIDStr = idStr
		} else if idStr, ok := claims["userID"].(string); ok {
			userIDStr = idStr
		} else if idStr, ok := claims["user_id"].(string); ok {
			userIDStr = idStr
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
			c.Abort()
			return
		}

		isAdmin, ok := claims["isAdmin"].(bool)
		if !ok {
			isAdmin = false
		}
		c.Set("userID", userIDStr)
		c.Set("isAdmin", isAdmin)
		c.Next()
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
		c.Abort()
	}
}

func generateRandomID() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func uploadStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	userIDStr := c.GetString("userID")

	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID format"})
		return
	}

	title := c.PostForm("title")
	description := c.PostForm("description")

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to get file: %v", err)})
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to open uploaded file: %v", err)})
		return
	}
	defer file.Close()

	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("target_service", "static-site"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write target_service field for SFSP"})
		return
	}

	part, err := writer.CreateFormFile("file", fileHeader.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create form for SFSP"})
		return
	}

	if _, err := file.Seek(0, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}
	_, err = io.Copy(part, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to copy file for SFSP"})
		return
	}
	writer.Close()

	sfspRequest, err := http.NewRequestWithContext(reqCtx, "POST", sfspApiUrl+"/api/v1/files", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create SFSP request"})
		return
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: time.Second * 30}
	sfspResponse, err := client.Do(sfspRequest)
	if err != nil {
		logger.Errorf("Error forwarding file to SFSP: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File scanning service is unavailable"})
		return
	}
	defer sfspResponse.Body.Close()

	respBytes, err := io.ReadAll(sfspResponse.Body)
	if err != nil {
		logger.Errorf("Failed to read SFSP response body: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response from scanning service"})
		return
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		logger.Errorf("SFSP returned non-2xx status: %d, body: %s", sfspResponse.StatusCode, string(respBytes))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to submit file for scanning", "details": string(respBytes)})
		return
	}

	var sfspRespData SfspUploadResponse
	if err := json.Unmarshal(respBytes, &sfspRespData); err != nil {
		logger.Errorf("Failed to decode SFSP response JSON: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode SFSP response"})
		return
	}

	siteID, err := generateRandomID()
	if err != nil {
		logger.Errorf("ERROR: Failed to generate static site ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate unique ID"})
		return
	}

	thumbnailHeader, err := c.FormFile("thumbnail")
	var thumbnailURL string
	if err == nil {
		thumbnailSrc, err := thumbnailHeader.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open thumbnail file"})
			return
		}
		defer thumbnailSrc.Close()

		thumbnailObjectName := fmt.Sprintf("thumbnails/%s%s", siteID, filepath.Ext(thumbnailHeader.Filename))
		_, err = minioClient.PutObject(reqCtx, minioBucket, thumbnailObjectName, thumbnailSrc, thumbnailHeader.Size, minio.PutObjectOptions{
			ContentType: thumbnailHeader.Header.Get("Content-Type"),
		})
		if err != nil {
			logger.Errorf("ERROR: Failed to upload thumbnail to MinIO: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload thumbnail to storage"})
			return
		}
		thumbnailURL = fmt.Sprintf("/static-sites/thumbnails/%s%s", siteID, filepath.Ext(thumbnailHeader.Filename))
	}

	sfspJobID, err := uuid.Parse(sfspRespData.JobID)
	if err != nil {
		logger.Errorf("Invalid Job ID from SFSP: %v (job_id=%q)", err, sfspRespData.JobID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid Job ID from SFSP"})
		return
	}

	_, err = db.ExecContext(reqCtx,
		"INSERT INTO static_sites (id, user_id, title, description, status, sfsp_job_id, minio_path, thumbnail_url, created_at, updated_at) VALUES ($1, $2, $3, $4, 'scanning', $5, $6, $7, NOW(), NOW())",
		siteID, userUUID, title, description, sfspJobID, "", thumbnailURL)
	if err != nil {
		logger.Errorf("ERROR: Failed to insert static site into DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register static site"})
		return
	}

	if sfspResponse.StatusCode == http.StatusOK && strings.ToLower(sfspRespData.Status) == "clean" {
		fileID, err := uuid.Parse(sfspRespData.FileID)
		if err != nil {
			logger.Errorf("Invalid File ID from SFSP: %v (file_id=%q)", err, sfspRespData.FileID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid File ID from SFSP"})
			return
		}

		event := event.ScanCompletionEvent{
			JobID:         sfspJobID,
			FileID:        fileID,
			FinalStatus:   "clean",
			ScannedAt:     time.Now().UTC(),
			SHA256:        sfspRespData.SHA256,
			Filename:      fileHeader.Filename,
			TargetService: "static-site",
		}
		if err := queue.EnqueueScanCompletionEvent(reqCtx, event); err != nil {
			logger.Errorf("CRITICAL: Failed to re-publish completion event for duplicate clean file: %v", err)
		} else {
			logger.Infof("Re-published completion event for existing clean file, job %s", sfspRespData.JobID)
		}
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "Static site upload initiated for scanning", "siteId": siteID})
}

func listStaticSitesHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	searchTerm := c.Query("q")
	var rows *sql.Rows
	var err error

	baseQuery := "SELECT id, user_id, title, description, minio_path, status, entry_point_path, thumbnail_url, created_at, updated_at FROM static_sites WHERE status = 'public'"
	if searchTerm != "" {
		rows, err = db.QueryContext(reqCtx, baseQuery+" AND title ILIKE $1 ORDER BY created_at DESC", "%"+searchTerm+"%")
	} else {
		rows, err = db.QueryContext(reqCtx, baseQuery+" ORDER BY created_at DESC")
	}

	if err != nil {
		logger.Errorf("ERROR: Database query failed in listStaticSitesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve static sites"})
		return
	}
	defer rows.Close()

	var staticSites []StaticSite
	for rows.Next() {
		var s StaticSite
		if err := rows.Scan(&s.ID, &s.UserID, &s.Title, &s.Description, &s.MinioPath, &s.Status, &s.EntryPointPath, &s.ThumbnailURL, &s.CreatedAt, &s.UpdatedAt); err != nil {
			logger.Errorf("Error scanning static site row: %v", err)
			continue
		}
		staticSites = append(staticSites, s)
	}
	c.JSON(http.StatusOK, staticSites)
}

func getStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	var s StaticSite
	err := db.QueryRowContext(reqCtx, "SELECT id, user_id, title, description, minio_path, status, entry_point_path, thumbnail_url, created_at, updated_at FROM static_sites WHERE id = $1", siteID).Scan(
		&s.ID, &s.UserID, &s.Title, &s.Description, &s.MinioPath, &s.Status, &s.EntryPointPath, &s.ThumbnailURL, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		} else {
			logger.Errorf("ERROR: Database query failed in getStaticSiteHandler: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve static site"})
		}
		return
	}
	c.JSON(http.StatusOK, s)
}

func updateStaticSiteHandler(c *gin.Context) {
	reqCtx := c.Request.Context()
	siteID := c.Param("id")
	userIDStr := c.GetString("userID")
	isAdmin := c.GetBool("isAdmin")

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Errorf("ERROR: updateStaticSiteHandler JSON bind error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request format: %v", err)})
		return
	}

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

	// Perform the update
	_, err = db.ExecContext(reqCtx,
		"UPDATE static_sites SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
		req.Title, req.Description, siteID)
	if err != nil {
		logger.Errorf("ERROR: Failed to update static site %s in DB: %v", siteID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Static site updated successfully"})
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