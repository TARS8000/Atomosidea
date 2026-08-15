package scanner

import (
	"context"

	"github.com/atmosidea/sfsp/internal/sandbox" // Import the sandbox package
)

// ScanResult represents the result of a single scan operation
type ScanResult struct {
	Result    string          `json:"result"` // e.g., clean, suspicious, malicious, error
	Details   string          `json:"details,omitempty"`
	RawOutput *map[string]any `json:"raw_output,omitempty"`
}

// Scanner defines the interface for all file scanners
type Scanner interface {
	// Scan performs a scan on the given file path using the provided sandbox.
	Scan(ctx context.Context, sb sandbox.Sandbox, filePath string) (ScanResult, error)
}
