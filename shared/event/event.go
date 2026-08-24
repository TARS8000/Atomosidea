package event

import (
	"time"

	"github.com/google/uuid"
)

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