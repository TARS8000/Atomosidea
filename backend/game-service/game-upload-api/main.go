package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql" // <-- これを追加
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
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
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Status             string     `json:"status"`
	GameURL            string     `json:"game_url"`
	ThumbnailURL       string     `json:"thumbnail_url"`
	ThumbnailSfspJobID *uuid.UUID `json:"thumbnail_sfsp_job_id,omitempty"`
	UploaderID         string      `json:"uploader_id"`
	UploaderName       string      `json:"uploader_name"`
	TeamID             string      `json:"team_id"`
	Scale              float32     `json:"scale"`
	OffsetX            int         `json:"offset_x"`
	OffsetY            int         `json:"offset_y"`
	NativeWidth        int         `json:"native_width"`
	NativeHeight       int         `json:"native_height"`
	CreatedAt          time.Time   `json:"created_at"`
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

// VerificationResult はZIPファイルの検証結果とデバッグ情報を含みます
type VerificationResult struct {
	IsValid           bool   `json:"is_valid"`
	WasmFileFound     bool   `json:"wasm_file_found"`
	UnityKeywordFound bool   `json:"unity_keyword_found"`
	CheckedFile       string `json:"checked_file"`
}

func generateRandomID() (string, error) {
	for {
		bytes := make([]byte, 5)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		id := hex.EncodeToString(bytes)
		// Ensure the ID always contains at least one alphabetic character so it is
		// unambiguously alphanumeric and never purely numeric.
		if strings.ContainsAny(id, "abcdef") {
			return id, nil
		}
	}
}

func uploadToSFSP(ctx context.Context, tmpFile *os.File, header *multipart.FileHeader, targetService string, resp *SfspUploadResponse) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("target_service", targetService); err != nil {
		return fmt.Errorf("failed to write target_service: %w", err)
	}

	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		return fmt.Errorf("failed to create form: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to reset file reader: %w", err)
	}

	if _, err := io.Copy(part, tmpFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}
	writer.Close()

	sfspRequest, err := http.NewRequestWithContext(ctx, "POST", sfspApiUrl+"/api/v1/files", body)
	if err != nil {
		return fmt.Errorf("failed to create SFSP request: %w", err)
	}
	sfspRequest.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 10 * time.Minute}
	sfspResponse, err := client.Do(sfspRequest)
	if err != nil {
		return fmt.Errorf("SFSP request failed: %w", err)
	}
	defer sfspResponse.Body.Close()

	respBytes, err := io.ReadAll(sfspResponse.Body)
	if err != nil {
		return fmt.Errorf("failed to read SFSP response: %w", err)
	}

	if sfspResponse.StatusCode < 200 || sfspResponse.StatusCode >= 300 {
		return fmt.Errorf("SFSP returned non-2xx status %d: %s", sfspResponse.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return fmt.Errorf("failed to decode SFSP response: %w", err)
	}

	return nil
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

	// 既存DBへの後方互換性のため、team_idカラムが存在しなければ追加する。
	// team_idがNULLのゲームが全体公開、チームトークンを保持するゲームがそのチーム限定公開になる。
	if err := ensureGameSchema(ctx, db); err != nil {
		logger.Fatalf("Failed to ensure game schema: %v", err)
	}

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
		api.DELETE("/:id/thumbnail", authMiddleware(), deleteThumbnailHandler)
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
	teamID := c.PostForm("team_id")
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

	// ファイル形式チェック (ZIP)
	buffer := make([]byte, 512)
	_, err = gameFile.Read(buffer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file for type detection"})
		return
	}
	if _, err := gameFile.Seek(0, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader"})
		return
	}
	contentType := http.DetectContentType(buffer)
	isZipExtension := strings.HasSuffix(strings.ToLower(gameFileHeader.Filename), ".zip")

	if contentType != "application/zip" && !isZipExtension {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error":                 "Unsupported file type. Only ZIP files are allowed.",
			"detected_content_type": contentType,
			"filename":              gameFileHeader.Filename,
			"is_zip_extension":      isZipExtension,
		})
		return
	}

	// Unity WebGLビルドのZIPファイルかどうかの検証
	verificationResult, err := verifyUnityWebGLZip(gameFile, gameFileHeader.Size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to verify ZIP file: %v", err)})
		return
	}
	if !verificationResult.IsValid {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": "The uploaded ZIP file is not a valid Unity WebGL build.",
			"debug_info": verificationResult,
		})
		return
	}
	if _, err := gameFile.Seek(0, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset file reader after verification"})
		return
	}

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

	var thumbnailSFSPJobID *uuid.UUID
	var thumbnailURL string
	thumbnailFile, thumbnailHeader, err_thumb := c.Request.FormFile("thumbnail")
	if err_thumb == nil {
		defer thumbnailFile.Close()

		tmpThumbFile, err := os.CreateTemp("", "sfsp-thumbnail-*.tmp")
		if err != nil {
			logger.Errorf("Failed to create temp file for thumbnail SFSP upload: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
			return
		}
		defer os.Remove(tmpThumbFile.Name())
		defer tmpThumbFile.Close()

		if _, err := io.Copy(tmpThumbFile, thumbnailFile); err != nil {
			logger.Errorf("Failed to copy thumbnail to temp file: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
			return
		}
		if _, err := tmpThumbFile.Seek(0, io.SeekStart); err != nil {
			logger.Errorf("Failed to reset temp thumbnail file: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
			return
		}

		var thumbSfspResp SfspUploadResponse
if err := uploadToSFSP(c.Request.Context(), tmpThumbFile, thumbnailHeader, "game-thumbnail", &thumbSfspResp); err != nil {
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
		thumbnailSFSPJobID = &parsedThumbJobID

		if sfspResponse.StatusCode == http.StatusOK && strings.ToLower(thumbSfspResp.Status) == "clean" {
			thumbFileID, err := uuid.Parse(thumbSfspResp.FileID)
			if err == nil {
				event := event.ScanCompletionEvent{
					JobID:         parsedThumbJobID,
					FileID:        thumbFileID,
					FinalStatus:   "clean",
					ScannedAt:     time.Now().UTC(),
					SHA256:        thumbSfspResp.SHA256,
					Filename:      thumbnailHeader.Filename,
					TargetService: "game-thumbnail",
				}
				if err := queue.EnqueueScanCompletionEvent(context.Background(), event); err != nil {
					logger.Errorf("CRITICAL: Failed to re-publish thumbnail completion event for duplicate clean file: %v", err)
				} else {
					logger.Infof("Re-published thumbnail completion event for existing clean file, job %s", thumbSfspResp.JobID)
				}
			}
		}
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

teamIDVal := interface{}(teamID)
	if teamID == "" {
		teamIDVal = nil
	}
	_, err = db.Exec(
		context.Background(),
		`INSERT INTO games
        (id, user_id, title, description, status, sfsp_job_id, thumbnail_sfsp_job_id, thumbnail_url, team_id)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		gameID,
		userUUID,
		title,
		description,
		gameStatus,
		sfspJobID,
		thumbnailSFSPJobID,
		thumbnailURL,
		teamIDVal,
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

func verifyUnityWebGLZip(file multipart.File, size int64) (*VerificationResult, error) {
	result := &VerificationResult{}

	zipReader, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("failed to read zip file: %w", err)
	}

	for _, f := range zipReader.File {
		isWasm := strings.HasSuffix(f.Name, ".wasm")
		isBrotli := strings.HasSuffix(f.Name, ".wasm.br")
		isGzip := strings.HasSuffix(f.Name, ".wasm.gz") // .gz も考慮

		if isWasm || isBrotli || isGzip {
			result.WasmFileFound = true
			result.CheckedFile = f.Name

			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open wasm file in zip: %w", err)
			}
			defer rc.Close()

			var reader io.Reader = rc
			if isBrotli {
				reader = brotli.NewReader(rc)
			}
			// ここで .gz の解凍ロジックも追加可能

			scanner := bufio.NewScanner(reader)
			buf := make([]byte, 0, 64*1024)
			scanner.Buffer(buf, 1024*1024) // 1MBのバッファ

			for scanner.Scan() {
				if bytes.Contains(scanner.Bytes(), []byte("il2cpp")) || bytes.Contains(scanner.Bytes(), []byte("UnityEngine")) {
					result.UnityKeywordFound = true
					break
				}
			}
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error scanning wasm file: %w", err)
			}
			if result.UnityKeywordFound {
				break
			}
		}
	}

	result.IsValid = result.WasmFileFound && result.UnityKeywordFound
	return result, nil
}

func listGamesHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	teamToken := c.Query("team")

	// team パラメータ: ある場合はそのチーム限定(team_id = $)、なければ公開(team_id IS NULL)のみ。
	// status='public' のみを表示。プレースホルダーは args の順序に合わせて番号を振る。
	whereConds := []string{"status = 'public'"}
	var args []interface{}
	argIdx := 1
	if teamToken != "" {
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
		`SELECT id, title, description, status, COALESCE(game_url, ''), COALESCE(thumbnail_url, ''), thumbnail_sfsp_job_id, user_id, scale, offset_x, offset_y, native_width, native_height, created_at
        FROM games
        WHERE id = $1`, gameID).Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.GameURL, &g.ThumbnailURL, &g.ThumbnailSfspJobID, &g.UploaderID, &g.Scale, &g.OffsetX, &g.OffsetY, &g.NativeWidth, &g.NativeHeight, &g.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		} else {
			logger.Errorf("ERROR: Database query failed in gameDetailsHandler: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve game"})
		}
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

	_, err = db.Exec(context.Background(),
		"UPDATE games SET title = $1, description = $2, updated_at = NOW() WHERE id = $3",
		title, description, gameID)
	if err != nil {
		logger.Errorf("Error updating game details: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update game details"})
		return
	}

	thumbnailFile, thumbnailHeader, err_thumb := c.Request.FormFile("thumbnail")
	if err_thumb == http.ErrMissingFile {
		c.JSON(http.StatusOK, gin.H{"message": "Game details updated successfully"})
		return
	}
	if err_thumb != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thumbnail file"})
		return
	}
	defer thumbnailFile.Close()

	tmpThumbFile, err := os.CreateTemp("", "sfsp-thumbnail-*.tmp")
	if err != nil {
		logger.Errorf("Failed to create temp file for thumbnail SFSP upload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
		return
	}
	defer os.Remove(tmpThumbFile.Name())
	defer tmpThumbFile.Close()

	if _, err := io.Copy(tmpThumbFile, thumbnailFile); err != nil {
		logger.Errorf("Failed to copy thumbnail to temp file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
		return
	}
	if _, err := tmpThumbFile.Seek(0, io.SeekStart); err != nil {
		logger.Errorf("Failed to reset temp thumbnail file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process thumbnail"})
		return
	}

	var thumbSfspResp SfspUploadResponse
	if err := uploadToSFSP(c.Request.Context(), tmpThumbFile, thumbnailHeader, "game-thumbnail", &thumbSfspResp); err != nil {
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

	if strings.ToLower(thumbSfspResp.Status) == "clean" {
		if thumbFileID, err := uuid.Parse(thumbSfspResp.FileID); err == nil {
			event := event.ScanCompletionEvent{
				JobID:         parsedThumbJobID,
				FileID:        thumbFileID,
				FinalStatus:   "clean",
				ScannedAt:     time.Now().UTC(),
				SHA256:        thumbSfspResp.SHA256,
				Filename:      thumbnailHeader.Filename,
				TargetService: "game-thumbnail",
			}
			if err := queue.EnqueueScanCompletionEvent(context.Background(), event); err != nil {
				logger.Errorf("CRITICAL: Failed to re-publish thumbnail completion event for existing clean file: %v", err)
			} else {
				logger.Infof("Re-published thumbnail completion event for existing clean file, job %s", parsedThumbJobID)
			}
		}
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "Thumbnail upload accepted for scanning",
		"job_id":  parsedThumbJobID.String(),
		"status":  "scanning",
	})
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

	// Thumbnail is stored as a sibling object (thumbnails/<gameID>), not under
	// games/<gameID>/, so delete it explicitly to avoid orphaned files.
	thumbObjectName := fmt.Sprintf("thumbnails/%s", gameID)
	if err := minioClient.RemoveObject(ctx, bucketName, thumbObjectName, minio.RemoveObjectOptions{}); err != nil {
		logger.Errorf("Error deleting thumbnail %s for game %s: %v", thumbObjectName, gameID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete game thumbnail from storage"})
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

func deleteThumbnailHandler(c *gin.Context) {
	gameID := c.Param("id")
	if gameID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid game ID"})
		return
	}

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
