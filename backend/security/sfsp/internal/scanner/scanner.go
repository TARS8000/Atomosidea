package scanner

// ScanResult は1回のスキャン（ClamAV または YARA）の結果を表す。
// RawOutput にはサンドボックスで実行したコマンドの stdout / stderr が入る。
type ScanResult struct {
	Result    string          `json:"result"` // 例: clean, suspicious, malicious, error
	Details   string          `json:"details,omitempty"`
	RawOutput *map[string]any `json:"raw_output,omitempty"`
}
