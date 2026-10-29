package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/model"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// APIKeyHeader is the header every /api/v1/* request must carry.
const APIKeyHeader = "X-SFSP-API-Key"

// API holds dependencies for API handlers
type API struct {
	Config config.Config
	Logger *zap.SugaredLogger
}

// NewAPI creates a new API instance
func NewAPI(cfg config.Config, logger *zap.SugaredLogger) *API {
	return &API{
		Config: cfg,
		Logger: logger,
	}
}

// RequireAPIKey rejects requests whose X-SFSP-API-Key header does not match expected.
// Both values are hashed before the constant-time comparison so that neither the
// content nor the length of the key leaks through timing.
// It runs before the handler, so an unauthenticated upload body is never read.
func RequireAPIKey(expected string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(expected))
	return func(c *gin.Context) {
		provided := c.GetHeader(APIKeyHeader)
		got := sha256.Sum256([]byte(provided))
		if provided == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			c.Header("Connection", "close")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

var allowedServices = map[string]bool{
	"static-site":     true,
	"game":            true,
	"stream":          true,
	"profile":         true,
	"video-thumbnail": true,
	"game-thumbnail":  true,
	"site-thumbnail":  true,
}

var errUploadTooLarge = errors.New("uploaded file exceeds the size limit")

// countingLimitReader counts bytes and fails once more than max bytes were read.
type countingLimitReader struct {
	r        io.Reader
	n        int64
	max      int64
	exceeded bool
}

func (l *countingLimitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.max {
		l.exceeded = true
		return n, errUploadTooLarge
	}
	return n, err
}

func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe) || errors.Is(err, errUploadTooLarge) ||
		(err != nil && strings.Contains(err.Error(), "request body too large"))
}

// HandleFileUpload handles file uploads, streams them to MinIO, and enqueues a scan job.
// The multipart body is read part by part (MultipartReader) and the file part is streamed
// straight to MinIO while its SHA256 is computed, so the upload is never held in memory
// nor copied to a temporary file.
func (a *API) HandleFileUpload(c *gin.Context) {
	maxUpload := a.Config.Limits.MaxUploadBytes
	// フォームのフィールドや境界の分として 1 MiB の余裕を持たせる
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUpload+(1<<20))

	mr, err := c.Request.MultipartReader()
	if err != nil {
		a.Logger.Errorf("Failed to read multipart request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "multipart/form-data request is required"})
		return
	}

	ctx := c.Request.Context()
	rawBucket := a.Config.MinIO.Buckets.RawFiles

	var (
		targetService string
		filename      string
		contentType   string
		fileSHA256    string
		fileSize      int64
		fileID        uuid.UUID
		objectName    string
		uploaded      bool
		committed     bool
	)
	// 途中で失敗した場合は raw-files に置いたオブジェクトを消す
	defer func() {
		if uploaded && !committed {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := storage.DeleteObject(delCtx, rawBucket, objectName); err != nil {
				a.Logger.Warnf("Failed to remove orphaned raw object %s: %v", objectName, err)
			}
		}
	}()

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isBodyTooLarge(err) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds the maximum size of %d bytes", maxUpload)})
				return
			}
			a.Logger.Errorf("Failed to read multipart part: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Malformed multipart request"})
			return
		}

		switch part.FormName() {
		case "target_service":
			b, err := io.ReadAll(io.LimitReader(part, 256))
			part.Close()
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read target_service"})
				return
			}
			targetService = strings.TrimSpace(string(b))

		case "file":
			if uploaded {
				part.Close()
				c.JSON(http.StatusBadRequest, gin.H{"error": "Only one file is allowed"})
				return
			}
			filename = part.FileName() // multipart は filepath.Base を適用済み
			if filename == "" || filename == "." || filename == "/" {
				part.Close()
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file from request"})
				return
			}
			// target_service が先に来ていれば、ストリーミング前に検証する
			if targetService != "" && !allowedServices[targetService] {
				part.Close()
				a.rejectTargetService(c, targetService)
				return
			}
			contentType = part.Header.Get("Content-Type")
			fileID = uuid.New()
			// MinIO のオブジェクトキーを FileID の文字列単体に統一
			objectName = fileID.String()

			hash := sha256.New()
			counter := &countingLimitReader{r: part, max: maxUpload}
			a.Logger.Infof("Streaming upload to MinIO bucket '%s' with key '%s'", rawBucket, objectName)
			info, err := storage.UploadStream(ctx, rawBucket, objectName, io.TeeReader(counter, hash), -1, contentType)
			part.Close()
			if err != nil {
				// 部分的に書かれたオブジェクトが残っていれば defer で消す
				uploaded = true
				if counter.exceeded || isBodyTooLarge(err) {
					c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds the maximum size of %d bytes", maxUpload)})
					return
				}
				a.Logger.Errorf("Failed to upload file to MinIO (Bucket: %s, Key: %s): %v", rawBucket, objectName, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store file"})
				return
			}
			uploaded = true
			fileSize = info.Size
			if fileSize <= 0 {
				fileSize = counter.n
			}
			fileSHA256 = hex.EncodeToString(hash.Sum(nil))

		default:
			// 未知のフィールドは読み捨てる（MaxBytesReader で上限あり）
			part.Close()
		}
	}

	if !uploaded {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file from request"})
		return
	}
	if targetService == "" {
		a.Logger.Errorf("target_service is missing in form data")
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_service is required"})
		return
	}
	if !allowedServices[targetService] {
		a.rejectTargetService(c, targetService)
		return
	}

	fileType := determineFileType(filename, contentType)

	newFile := model.File{
		ID:            fileID,
		Filename:      filename,
		Filesize:      fileSize,
		MimeType:      contentType,
		SHA256:        fileSHA256,
		StoragePath:   fmt.Sprintf("%s/%s", rawBucket, objectName),
		FileType:      fileType,
		TargetService: targetService,
		CreatedAt:     time.Now(),
	}
	_, err = database.DB.Exec(ctx,
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
	_, err = database.DB.Exec(ctx,
		"INSERT INTO scan_jobs (id, file_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
		newJob.ID, newJob.FileID, newJob.Status, newJob.CreatedAt, newJob.UpdatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to create scan job in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create scan job"})
		return
	}

	if err := queue.EnqueueJob(ctx, newJob.ID.String()); err != nil {
		a.Logger.Errorf("Failed to enqueue job to Redis: %v", err)
		// キューに入らなかったジョブが queued のまま残らないようにする
		if _, dbErr := database.DB.Exec(context.Background(), "UPDATE scan_jobs SET status = 'failed', processing_details = $1 WHERE id = $2", "Failed to enqueue scan job", newJob.ID); dbErr != nil {
			a.Logger.Errorf("Failed to mark job %s as failed: %v", newJob.ID, dbErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enqueue scan job"})
		return
	}
	committed = true

	a.Logger.Infof("Successfully uploaded file and enqueued job: FileID=%s, JobID=%s, Bucket=%s, ObjectKey=%s, FileType=%s, Size=%d", newFile.ID, newJob.ID, rawBucket, objectName, fileType, fileSize)
	c.JSON(http.StatusAccepted, model.FileUploadResponse{
		FileID: newFile.ID,
		JobID:  newJob.ID,
		Status: newJob.Status,
	})
}

func (a *API) rejectTargetService(c *gin.Context, targetService string) {
	a.Logger.Errorf("Invalid target_service: %s", targetService)
	c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid target_service: %s. Allowed values are 'static-site', 'game', 'stream', 'profile', 'video-thumbnail', 'game-thumbnail', 'site-thumbnail'", targetService)})
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
	case ext == ".zip" || mimeType == "application/zip" || mimeType == "application/x-zip-compressed":
		return "zip"
	case strings.HasPrefix(mimeType, "video/") || ext == ".mp4" || ext == ".mkv" || ext == ".webm" || ext == ".mov" || ext == ".avi":
		return "video"
	case ext == ".html" || ext == ".htm" || mimeType == "text/html":
		return "html"
	default:
		return "other"
	}
}
