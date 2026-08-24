package scanner

import (
	"context"
	"fmt"
	"strings"

	"github.com/atmosidea/sfsp/internal/sandbox"
	"go.uber.org/zap"
)

// ClamAVScanner implements the Scanner interface for ClamAV
type ClamAVScanner struct {
	clamscanImage string // Docker image for clamscan client
	logger        *zap.SugaredLogger
}

// NewClamAVScanner creates a new ClamAVScanner instance
func NewClamAVScanner(clamscanImage string, logger *zap.SugaredLogger) *ClamAVScanner {
	return &ClamAVScanner{
		clamscanImage: clamscanImage,
		logger:        logger,
	}
}

// Scan performs a ClamAV scan on the given file path using the provided sandbox.
func (c *ClamAVScanner) Scan(ctx context.Context, sb sandbox.Sandbox, jobID string, filePath string) (ScanResult, error) {
	command := []string{"clamscan", "--no-summary", "{FILE_PATH}"}

	stdout, stderr, err := sb.RunInSandbox(ctx, jobID, c.clamscanImage, filePath, command)
	if err != nil {
		// If sandbox command exited with non-zero status, but stdout/stderr might have info
		if strings.Contains(stderr, "No supported database files found") {
			return ScanResult{Result: "error", Details: "ClamAV database files not found in sandbox"}, err
		}
		if strings.Contains(stdout, "FOUND") {
			parts := strings.Split(stdout, ":")
			threat := "Unknown Threat"
			if len(parts) > 1 {
				threat = strings.TrimSpace(strings.Split(parts[1], " FOUND")[0])
			}
			return ScanResult{Result: "malicious", Details: fmt.Sprintf("Threat found: %s", threat), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
		}
		// If stdout is empty and exit code is 0, it means no threats were found.
		if stdout == "" && err == nil {
			return ScanResult{Result: "clean", Details: "No threats found"}, nil
		}
		c.logger.Errorf("ClamAV sandbox execution failed for file %s: %v, stdout: %s, stderr: %s", filePath, err, stdout, stderr)
		return ScanResult{Result: "error", Details: fmt.Sprintf("ClamAV sandbox execution failed: %v", err)}, err
	}

	// Change Debugf to Infof temporarily to see the output in logs
	c.logger.Infof("ClamAV sandbox stdout: %s", stdout)
	c.logger.Infof("ClamAV sandbox stderr: %s", stderr)

	if strings.Contains(stdout, "FOUND") {
		parts := strings.Split(stdout, ":")
		threat := "Unknown Threat"
		if len(parts) > 1 {
			threat = strings.TrimSpace(strings.Split(parts[1], " FOUND")[0])
		}
		return ScanResult{Result: "malicious", Details: fmt.Sprintf("Threat found: %s", threat), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	} else if strings.Contains(stdout, "OK") || stdout == "" { // Also treat empty stdout as clean
		return ScanResult{Result: "clean", Details: "No threats found", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	} else if strings.Contains(stderr, "ERROR") || strings.Contains(stdout, "ERROR") {
		return ScanResult{Result: "error", Details: fmt.Sprintf("ClamAV internal error: %s", stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("clamav internal error: %s", stderr)
	}

	// Improved error message to include stdout/stderr
	return ScanResult{Result: "error", Details: fmt.Sprintf("Unknown ClamAV sandbox response. stdout=[%s] stderr=[%s]", stdout, stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("unknown clamav sandbox response. stdout=[%s] stderr=[%s]", stdout, stderr)
}
