package scanner

// ScanResult represents the result of a single scan operation
type ScanResult struct {
	Result    string          `json:"result"` // e.g., clean, suspicious, malicious, error
	Details   string          `json:"details,omitempty"`
	RawOutput *map[string]any `json:"raw_output,omitempty"`
}
