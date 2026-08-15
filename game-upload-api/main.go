package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var (
	db          *pgxpool.Pool
	minioClient *minio.Client
	jwtSecret   []byte
	bucketName  string
	sfspApiUrl  string
	logger      *zap.SugaredLogger
)

type Game struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	GameURL      string    `json:"game_url"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	UploaderName string    `json:"uploader_name"`
	Scale        float32   `json:"scale"`
	OffsetX      int       `json:"offset_x"`
	OffsetY      int       `json:"offset_y"`
	NativeWidth  int       `json:"native_width"`
	NativeHeight int       `json:"native_height"`
	CreatedAt    time.Time `json:"created_at"`
}

type AdjustPayload struct {
	Scale   float32 `json:"scale"`
	OffsetX int     `json:"offset_x"`
	OffsetY int     `json:"offset_y"`
}

type SfspUploadResponse struct {
	FileID   string `json:"file_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
}

func generateRandomID() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer zapLogger.Sync()
	logger = zapLogger.Sugar()

	ctx := context.Background()
	databaseUrl := os.Getenv("DATABASE_URL")
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	minioAccessKey := os.Getenv("MINIO_ACCESS_KEY_ID")
	minioSecretKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	minioUseSSL := os.Getenv("MINIO_USE_SSL") == "true"
	bucketName = os.Getenv("MINIO_BUCKET_NAME")
	sfspApiUrl = os.Getenv("SFSP_API_URL")

	if sfspApiUrl == "" {
		logger.Fatalf("SFSP_API_URL environment variable is not set")
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	db, err = pgxpool.New(ctx, databaseUrl)
	if err != nil {
		logger.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

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

	r := gin.Default()
	api := r.Group("/api/games")
	{
		api.GET("", listGamesHandler)
		api.GET("/:id", gameDetailsHandler)
		api.POST("/upload", authMiddleware(), uploadGameHandler)
		api.PUT("/:id", authMiddleware(), updateGameHandler)
		api.DELETE("/:id", authMiddleware(), deleteGameHandler)
		api.PUT("/adjust/:id", authMiddleware(), adjustGameHandler)
	}

	logger.Info("Game API service (read/write) starting on port 8082")
	if err := r.Run(":8082"); err != nil {
		logger.Fatalf("Failed to run server: %v", err)
	}
}

func uploadGameHandler(c *gin.Context) {
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

	title := c.PostForm("title")
	description := c.PostForm("description")
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title is required"})
		return
	}

	gameFile, gameFileHeader, err := c.Request.FormFile("game")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Game ZIP file is required"})
		return
	}
	defer gameFile.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("target_service", "game"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write target_service field for SFSP"})
		return
	}

	part, err := writer.CreateFormFile("file", gameFileHeader.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create form for SFSP"})
		return
	}

	if _, err := gameFile.Seek(0, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}

	_, err = io.Copy(part, gameFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to copy file for SFSP"})
		return
	}
	writer.Close()

	sfspRequest, err := http.NewRequest("POST", sfspApiUrl+"/api/v1/files", body)
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

	logger.Infof(
		"SFSP response: code=%d file_id=%s job_id=%s status=%s",
		sfspResponse.StatusCode,
		sfspRespData.FileID,
		sfspRespData.JobID,
		sfspRespData.Status,
	)

	var thumbnailURL string
	thumbnailFile, thumbnailHeader, err_thumb := c.Request.FormFile("thumbnail")
	if err_thumb == nil {
		defer thumbnailFile.Close()
		thumbnailObjectName := fmt.Sprintf("thumbnails/%s-%s", uuid.New().String(), thumbnailHeader.Filename)
		_, err_put := minioClient.PutObject(context.Background(), bucketName, thumbnailObjectName, thumbnailFile, thumbnailHeader.Size, minio.PutObjectOptions{})
		if err_put != nil {
			logger.Errorf("Error uploading thumbnail to MinIO: %v", err_put)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store thumbnail file"})
			return
		}
		thumbnailURL = fmt.Sprintf("/games/%s", thumbnailObjectName)
	} else if err_thumb != http.ErrMissingFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thumbnail file"})
		return
	}

	gameID, err := generateRandomID()
	if err != nil {
		logger.Errorf("Error generating random ID: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate game ID"})
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

	gameStatus := "scanning"
	if sfspResponse.StatusCode == http.StatusOK && strings.ToLower(sfspRespData.Status) == "clean" {
		gameStatus = "processing"
	}

	_, err = db.Exec(
		context.Background(),
		`INSERT INTO games
       (id, user_id, title, description, status, sfsp_job_id, thumbnail_url)
       VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		gameID,
		userUUID,
		title,
		description,
		gameStatus,
		sfspJobID,
		thumbnailURL,
	)
	if err != nil {
		logger.Errorf("Error creating initial game record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create game record"})
		return
	}

	if gameStatus == "processing" {
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
			Filename:      gameFileHeader.Filename,
			TargetService: "game",
		}
		if err := queue.EnqueueScanCompletionEvent(context.Background(), event); err != nil {
			logger.Errorf("CRITICAL: Failed to re-publish completion event for duplicate clean file: %v", err)
		} else {
			logger.Infof("Re-published completion event for existing clean file, job %s", sfspRespData.JobID)
		}
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Game upload accepted",
		"gameId":  gameID,
		"status":  gameStatus,
	})
}

func listGamesHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	var rows pgx.Rows
	var err error

	if searchTerm != "" {
		rows, err = db.Query(context.Background(),
			`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), user_id, scale, offset_x, offset_y, native_width, native_height, created_at
           FROM games
           WHERE status = 'public' AND title ILIKE $1
           ORDER BY created_at DESC LIMIT 50`, "%"+searchTerm+"%")
	} else {
		rows, err = db.Query(context.Background(),
			`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), user_id, scale, offset_x, offset_y, native_width, native_height, created_at
           FROM games
           WHERE status = 'public'
           ORDER BY created_at DESC LIMIT 50`)
	}

	if err != nil {
		logger.Errorf("ERROR: Database query failed in listGamesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing games"})
		return
	}
	defer rows.Close()

	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.GameURL, &g.ThumbnailURL, &g.UploaderID, &g.Scale, &g.OffsetX, &g.OffsetY, &g.NativeWidth, &g.NativeHeight, &g.CreatedAt); err != nil {
			logger.Errorf("Error scanning game row: %v", err)
			continue
		}
		games = append(games, g)
	}
	c.JSON(http.StatusOK, games)
}

func gameDetailsHandler(c *gin.Context) {
	gameID := c.Param("id")
	var g Game
	err := db.QueryRow(context.Background(),
		`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), user_id, scale, offset_x, offset_y, native_width, native_height, created_at
        FROM games
        WHERE id = $1`, gameID).Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.GameURL, &g.ThumbnailURL, &g.UploaderID, &g.Scale, &g.OffsetX, &g.OffsetY, &g.NativeWidth, &g.NativeHeight, &g.CreatedAt)
	if err != nil {
		logger.Errorf("ERROR: Database query failed in gameDetailsHandler: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}
	c.JSON(http.StatusOK, g)
}

func updateGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

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

	title := c.PostForm("title")
	description := c.PostForm("description")

	var thumbnailURL string
	thumbnailFile, thumbnailHeader, err_thumb := c.Request.FormFile("thumbnail")
	if err_thumb == nil {
		defer thumbnailFile.Close()
		thumbnailObjectName := fmt.Sprintf("thumbnails/%s-%s", uuid.New().String(), thumbnailHeader.Filename)
		_, err_put := minioClient.PutObject(context.Background(), bucketName, thumbnailObjectName, thumbnailFile, thumbnailHeader.Size, minio.PutObjectOptions{})
		if err_put != nil {
			logger.Errorf("Error uploading thumbnail to MinIO: %v", err_put)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store new thumbnail file"})
			return
		}
		thumbnailURL = fmt.Sprintf("/games/%s", thumbnailObjectName)
	} else if err_thumb != http.ErrMissingFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thumbnail file"})
		return
	}

	query := "UPDATE games SET title = $1, description = $2, updated_at = NOW()"
	args := []interface{}{title, description}
	argID := 3

	if thumbnailURL != "" {
		query += fmt.Sprintf(", thumbnail_url = $%d", argID)
		args = append(args, thumbnailURL)
		argID++
	}

	query += fmt.Sprintf(" WHERE id = $%d", argID)
	args = append(args, gameID)

	_, err = db.Exec(context.Background(), query, args...)
	if err != nil {
		logger.Errorf("Error updating game details: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update game details"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Game details updated successfully"})
}

func adjustGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

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

func deleteGameHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

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

	_, err = db.Exec(ctx, "DELETE FROM games WHERE id = $1", gameID)
	if err != nil {
		logger.Errorf("Error deleting game record %s from DB: %v", gameID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete game record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Game deleted successfully"})
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
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			var userID string
			if val, ok := claims["userID"].(string); ok {
				userID = val
			} else if val, ok := claims["user_id"].(string); ok {
				userID = val
			}

			if userID == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
				return
			}

			isAdmin, ok := claims["isAdmin"].(bool)
			if !ok {
				isAdmin = false
			}
			c.Set("userID", userID)
			c.Set("isAdmin", isAdmin)
			c.Next()
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		}
	}
}