// Package event はサービス間で Redis キュー経由でやり取りするイベントの型を定義する。
package event

import (
	"time"

	"github.com/google/uuid"
)

// ScanCompletionEvent はスキャン完了後に SFSP から各サービス向けキューへ発行されるイベント。
type ScanCompletionEvent struct {
	JobID         uuid.UUID `json:"job_id"`
	FileID        uuid.UUID `json:"file_id"`
	TargetService string    `json:"target_service"` // 例: "static-site", "game", "stream"
	FinalStatus   string    `json:"final_status"`   // 例: clean, malicious, suspicious, invalid
	ScannedAt     time.Time `json:"scanned_at"`
	SHA256        string    `json:"sha256"`
	Filename      string    `json:"filename"`
}
