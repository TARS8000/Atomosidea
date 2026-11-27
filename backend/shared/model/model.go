// Package model はファイル・スキャンジョブ・スキャン結果など、サービス間で共有するデータモデルを定義する。
package model

import (
	"time"

	"github.com/google/uuid"
)

// File はシステムにアップロードされたファイルのメタデータを表す。
type File struct {
	ID           uuid.UUID `json:"file_id"`
	Filename     string    `json:"filename"`
	Filesize     int64     `json:"filesize"`
	MimeType     string    `json:"mime_type"`
	SHA256       string    `json:"sha256"`
	StoragePath  string    `json:"storage_path"`
	FileType     string    `json:"file_type"` // 例: "video", "html", "zip", "other"
	TargetService string    `json:"target_service"` // 例: "static-site", "game", "stream", "other"
	CreatedAt    time.Time `json:"created_at"`
}

// ScanJob はファイル1件に対するスキャンジョブを表す。
type ScanJob struct {
	ID        uuid.UUID `json:"job_id"`
	FileID    uuid.UUID `json:"file_id"`
	Status    string    `json:"status"` // 例: queued, running, completed, failed, invalid
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScanResult は個々のスキャナー（ClamAV、YARA など）による1回分のスキャン結果を表す。
type ScanResult struct {
	ID        uuid.UUID       `json:"result_id"`
	JobID     uuid.UUID       `json:"job_id"`
	Scanner   string          `json:"scanner"` // 例: 'clamav', 'yara'
	Result    string          `json:"result"`  // 例: 'clean', 'suspicious', 'malicious', 'error'
	Details   *string         `json:"details,omitempty"`
	RawOutput *map[string]any `json:"raw_output,omitempty"`
	ScannedAt time.Time       `json:"scanned_at"`
}

// FileUploadResponse はファイルアップロード API のレスポンスボディ。
type FileUploadResponse struct {
	FileID uuid.UUID `json:"file_id"`
	JobID  uuid.UUID `json:"job_id"`
	Status string    `json:"status"`
}