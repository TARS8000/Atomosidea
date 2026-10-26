package scanner

import (
	"context"
	"fmt"
	"strings"

	"github.com/atmosidea/sfsp/internal/sandbox"
	"go.uber.org/zap"
)

// limitsExceededPrefix は --alert-exceeds-max=yes のときに上限超過で出る検出名の接頭辞
// （例: Heuristics.Limits.Exceeded.MaxFileSize）
const limitsExceededPrefix = "Heuristics.Limits.Exceeded"

// ClamAVLimits are passed to clamscan explicitly. clamscan's defaults are small (e.g.
// 100 MB max file size) and files over a limit are otherwise skipped and reported clean.
type ClamAVLimits struct {
	MaxFileSize     int64 // bytes
	MaxScanSize     int64 // bytes
	MaxFiles        int
	MaxRecursion    int
	MaxDirRecursion int
	MaxScanTimeMs   int64
	PCREMaxFileSize int64 // bytes
}

// ClamAVScanner implements the Scanner interface for ClamAV
type ClamAVScanner struct {
	clamscanImage string // Docker image for clamscan client
	limits        ClamAVLimits
	logger        *zap.SugaredLogger
}

// NewClamAVScanner creates a new ClamAVScanner instance
func NewClamAVScanner(clamscanImage string, limits ClamAVLimits, logger *zap.SugaredLogger) *ClamAVScanner {
	return &ClamAVScanner{
		clamscanImage: clamscanImage,
		limits:        limits,
		logger:        logger,
	}
}

func (c *ClamAVScanner) command() []string {
	l := c.limits
	cmd := []string{
		"clamscan",
		"-r", // ZIP展開後のディレクトリ配下（Build/ 等）もすべて再帰的に検査する
		"--no-summary",
		"--infected",
		// 上限を超えたファイルを「検査せずに clean」とせず、Heuristics.Limits.Exceeded.* として検出させる
		"--alert-exceeds-max=yes",
	}
	if l.MaxFileSize > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-filesize=%d", l.MaxFileSize))
	}
	if l.MaxScanSize > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-scansize=%d", l.MaxScanSize))
	}
	if l.MaxFiles > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-files=%d", l.MaxFiles))
	}
	if l.MaxRecursion > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-recursion=%d", l.MaxRecursion))
	}
	if l.MaxDirRecursion > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-dir-recursion=%d", l.MaxDirRecursion))
	}
	if l.MaxScanTimeMs > 0 {
		cmd = append(cmd, fmt.Sprintf("--max-scantime=%d", l.MaxScanTimeMs))
	}
	if l.PCREMaxFileSize > 0 {
		cmd = append(cmd, fmt.Sprintf("--pcre-max-filesize=%d", l.PCREMaxFileSize))
	}
	return append(cmd, "{FILE_PATH}")
}

// parseFound returns the signature names of every "<path>: <name> FOUND" line.
func parseFound(stdout string) []string {
	threats := []string{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, " FOUND") {
			continue
		}
		if idx := strings.LastIndex(line, ": "); idx >= 0 {
			threats = append(threats, strings.TrimSuffix(line[idx+2:], " FOUND"))
		}
	}
	return threats
}

// Scan performs a ClamAV scan on the given file path using the provided sandbox.
// clamscan exit codes: 0 = no virus found, 1 = virus(es) found, other = error.
// The verdict is decided by the exit code, never by "OK" substrings, so a partial
// error can not be misread as clean. Only exit code 0 is ever "clean".
func (c *ClamAVScanner) Scan(ctx context.Context, sb sandbox.Sandbox, jobID string, filePath string) (ScanResult, error) {
	command := c.command()

	stdout, stderr, err := sb.RunInSandbox(ctx, jobID, c.clamscanImage, filePath, command)
	rawOutput := &map[string]any{"stdout": stdout, "stderr": stderr}

	if err == nil {
		c.logger.Infof("ClamAV scan clean for %s", filePath)
		return ScanResult{Result: "clean", Details: "No threats found", RawOutput: rawOutput}, nil
	}

	code := sandbox.ExitCode(err)
	threats := parseFound(stdout)
	var real, limits []string
	for _, t := range threats {
		if strings.HasPrefix(t, limitsExceededPrefix) {
			limits = append(limits, t)
		} else {
			real = append(real, t)
		}
	}

	// 終了コード 2（一部エラー）でも実際の検出行があれば malicious として隔離する
	if (code == 1 || code == 2) && len(real) > 0 {
		return ScanResult{Result: "malicious", Details: fmt.Sprintf("Threat found: %s", strings.Join(threats, ", ")), RawOutput: rawOutput}, nil
	}

	// 上限超過（--alert-exceeds-max）。clamscan は MaxFileSize では終了コード 2、
	// MaxScanSize / MaxScanTime 等では 1 を返す。どちらも検査し切れていないので clean にはせず suspicious とする
	if (code == 1 || code == 2) && len(limits) > 0 {
		c.logger.Warnf("ClamAV scan limits exceeded for %s: %v", filePath, limits)
		return ScanResult{Result: "suspicious", Details: fmt.Sprintf("ClamAV scan limits exceeded (file not fully scanned): %s", strings.Join(limits, ", ")), RawOutput: rawOutput}, nil
	}

	// 終了コード 1 でも検出行が無い場合などは clamscan 以外の失敗とみなし、error（公開しない）として扱う
	details := fmt.Sprintf("ClamAV sandbox execution failed: %v", err)
	if strings.Contains(stderr, "No supported database files found") {
		details = "ClamAV database files not found in sandbox"
	}
	c.logger.Errorf("ClamAV sandbox execution failed for file %s: %v, stdout: %s, stderr: %s", filePath, err, stdout, stderr)
	return ScanResult{Result: "error", Details: details, RawOutput: rawOutput}, err
}
