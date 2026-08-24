package model

import (
	"time"

	"github.com/google/uuid"
)

// File represents a file uploaded to the system
type File struct {
	ID           uuid.UUID `json:"file_id"`
	Filename     string    `json:"filename"`
	Filesize     int64     `json:"filesize"`
	MimeType     string    `json:"mime_type"`
	SHA256       string    `json:"sha256"`
	StoragePath  string    `json:"storage_path"`
	FileType     string    `json:"file_type"` // e.g., "video", "html", "zip", "other"
	TargetService string    `json:"target_service"` // e.g., "static-site", "game", "stream", "other"
	CreatedAt    time.Time `json:"created_at"`
}

// ScanJob represents a scan job for a file
type ScanJob struct {
	ID        uuid.UUID `json:"job_id"`
	FileID    uuid.UUID `json:"file_id"`
	Status    string    `json:"status"` // e.g., queued, running, completed, failed, invalid
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScanResult represents the result from a specific scanner
type ScanResult struct {
	ID        uuid.UUID       `json:"result_id"`
	JobID     uuid.UUID       `json:"job_id"`
	Scanner   string          `json:"scanner"` // e.g., 'clamav', 'yara'
	Result    string          `json:"result"`  // e.g., 'clean', 'suspicious', 'malicious', 'error'
	Details   *string         `json:"details,omitempty"`
	RawOutput *map[string]any `json:"raw_output,omitempty"`
	ScannedAt time.Time       `json:"scanned_at"`
}

// FileUploadRequest is the request body for file upload API
type FileUploadRequest struct {
	File []byte `form:"file" binding:"required"`
}

// FileUploadResponse is the response body for file upload API
type FileUploadResponse struct {
	FileID uuid.UUID `json:"file_id"`
	JobID  uuid.UUID `json:"job_id"`
	Status string    `json:"status"`
}

// ScanCompletionEvent is the event published after a scan is finished
type ScanCompletionEvent struct {
	JobID       uuid.UUID `json:"job_id"`
	FileID      uuid.UUID `json:"file_id"`
	TargetService string    `json:"target_service"` // e.g., "static-site", "game", "stream"
	FinalStatus string    `json:"final_status"` // e.g., clean, malicious, suspicious, invalid
	ScannedAt   time.Time `json:"scanned_at"`
	SHA256      string    `json:"sha256"`
	Filename    string    `json:"filename"`
}