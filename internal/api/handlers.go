package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/config" // shared/config をインポート
	"github.com/atmosidea/shared/model"  // shared/model をインポート
	"github.com/atmosidea/shared/queue"  // shared/queue をインポート
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// API holds dependencies for API handlers
type API struct {
	Config config.Config // shared/config.Config に変更
	Logger *zap.SugaredLogger
}

// NewAPI creates a new API instance
func NewAPI(cfg config.Config, logger *zap.SugaredLogger) *API {
	return &API{
		Config: cfg,
		Logger: logger,
	}
}

// HandleFileUpload handles file uploads, saves to MinIO, and enqueues a scan job
func (a *API) HandleFileUpload(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		a.Logger.Errorf("Failed to get form file: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file from request"})
		return
	}

	targetService := c.PostForm("target_service")
	if targetService == "" {
		a.Logger.Errorf("target_service is missing in form data")
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_service is required"})
		return
	}

	allowedServices := map[string]bool{
		"static-site": true,
		"game":        true,
		"stream":      true,
	}
	if !allowedServices[targetService] {
		a.Logger.Errorf("Invalid target_service: %s", targetService)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid target_service: %s. Allowed values are 'static-site', 'game', 'stream'", targetService)})
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		a.Logger.Errorf("Failed to open uploaded file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open file"})
		return
	}
	defer file.Close()

	tempDir := os.TempDir()
	tempFilePath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	tempFile, err := os.Create(tempFilePath)
	if err != nil {
		a.Logger.Errorf("Failed to create temporary file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create temporary file"})
		return
	}
	defer os.Remove(tempFilePath)
	defer tempFile.Close()

	hash := sha256.New()
	mw := io.MultiWriter(tempFile, hash)
	if _, err := io.Copy(mw, file); err != nil {
		a.Logger.Errorf("Failed to copy file and calculate SHA256: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process file"})
		return
	}
	fileSHA256 := hex.EncodeToString(hash.Sum(nil))

	existingFile := model.File{}
	err = database.DB.QueryRow(c, "SELECT id, filename, filesize, mime_type, sha256, storage_path, file_type, target_service, created_at FROM files WHERE sha256 = $1", fileSHA256).Scan(
		&existingFile.ID, &existingFile.Filename, &existingFile.Filesize, &existingFile.MimeType, &existingFile.SHA256, &existingFile.StoragePath, &existingFile.FileType, &existingFile.TargetService, &existingFile.CreatedAt,
	)
	if err == nil {
		a.Logger.Infof("File with SHA256 %s already exists: %s", fileSHA256, existingFile.ID)
		var existingJobID uuid.UUID
		err = database.DB.QueryRow(c, "SELECT id FROM scan_jobs WHERE file_id = $1 ORDER BY created_at DESC LIMIT 1", existingFile.ID).Scan(&existingJobID)
		if err == nil {
			c.JSON(http.StatusOK, model.FileUploadResponse{
				FileID: existingFile.ID,
				JobID:  existingJobID,
				Status: "queued",
			})
			return
		}
	} else if err.Error() != "no rows in result set" {
		a.Logger.Errorf("Database error checking for existing file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	objectName := fmt.Sprintf("%s/%s", fileSHA256, fileHeader.Filename)
	uploadInfo, err := storage.UploadFile(c, a.Config.MinIO.Buckets.RawFiles, objectName, tempFilePath, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		a.Logger.Errorf("Failed to upload file to MinIO: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store file"})
		return
	}

	fileType := determineFileType(fileHeader.Filename, fileHeader.Header.Get("Content-Type"))

	newFile := model.File{
		ID:           uuid.New(),
		Filename:     fileHeader.Filename,
		Filesize:     uploadInfo.Size,
		MimeType:     fileHeader.Header.Get("Content-Type"),
		SHA256:       fileSHA256,
		StoragePath:  fmt.Sprintf("%s/%s", a.Config.MinIO.Buckets.RawFiles, objectName),
		FileType:     fileType,
		TargetService: targetService,
		CreatedAt:    time.Now(),
	}
	_, err = database.DB.Exec(c,
		"INSERT INTO files (id, filename, filesize, mime_type, sha256, storage_path, file_type, target_service, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		newFile.ID, newFile.Filename, newFile.Filesize, newFile.MimeType, newFile.SHA256, newFile.StoragePath, newFile.FileType, newFile.TargetService, newFile.CreatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to save file metadata to DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file metadata"})
		return
	}

	newJob := model.ScanJob{
		ID:        uuid.New(),
		FileID:    newFile.ID,
		Status:    "queued",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_, err = database.DB.Exec(c,
		"INSERT INTO scan_jobs (id, file_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
		newJob.ID, newJob.FileID, newJob.Status, newJob.CreatedAt, newJob.UpdatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to create scan job in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create scan job"})
		return
	}

	if err := queue.EnqueueJob(c, newJob.ID.String()); err != nil {
		a.Logger.Errorf("Failed to enqueue job to Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enqueue scan job"})
		return
	}

	a.Logger.Infof("File uploaded and job enqueued: FileID=%s, JobID=%s", newFile.ID, newJob.ID)
	c.JSON(http.StatusAccepted, model.FileUploadResponse{
		FileID: newFile.ID,
		JobID:  newJob.ID,
		Status: newJob.Status,
	})
}

func (a *API) HandleGetJob(c *gin.Context) {
	jobIDStr := c.Param("id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	var job model.ScanJob
	err = database.DB.QueryRow(c, "SELECT id, file_id, status, created_at, updated_at FROM scan_jobs WHERE id = $1", jobID).Scan(
		&job.ID, &job.FileID, &job.Status, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to get job from DB: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}

	c.JSON(http.StatusOK, job)
}

func (a *API) HandleGetResult(c *gin.Context) {
	jobIDStr := c.Param("id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	var results []model.ScanResult
	rows, err := database.DB.Query(c, "SELECT id, job_id, scanner, result, details, raw_output, scanned_at FROM scan_results WHERE job_id = $1", jobID)
	if err != nil {
		a.Logger.Errorf("Failed to get scan results from DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve scan results"})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var result model.ScanResult
		err := rows.Scan(&result.ID, &result.JobID, &result.Scanner, &result.Result, &result.Details, &result.RawOutput, &result.ScannedAt)
		if err != nil {
			a.Logger.Errorf("Failed to scan row for scan result: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process scan results"})
			return
		}
		results = append(results, result)
	}

	if len(results) == 0 {
		var jobStatus string
		err = database.DB.QueryRow(c, "SELECT status FROM scan_jobs WHERE id = $1", jobID).Scan(&jobStatus)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Job is still processing or has no results yet", "job_status": jobStatus})
		return
	}

	c.JSON(http.StatusOK, results)
}

func determineFileType(filename, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ext == ".zip" || mimeType == "application/zip":
		return "zip"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case ext == ".html" || ext == ".htm" || mimeType == "text/html":
		return "html"
	default:
		return "other"
	}
}