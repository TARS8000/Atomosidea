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
// It now takes the image name for the clamscan client that will run in the sandbox.
func NewClamAVScanner(clamscanImage string, logger *zap.SugaredLogger) *ClamAVScanner {
	return &ClamAVScanner{
		clamscanImage: clamscanImage,
		logger:        logger,
	}
}

// Scan performs a ClamAV scan on the given file path using the provided sandbox.
func (c *ClamAVScanner) Scan(ctx context.Context, sb sandbox.Sandbox, filePath string) (ScanResult, error) {
	// The command to execute inside the sandbox container
	// {FILE_PATH} will be replaced by the sandbox implementation with the actual mounted path
	// --no-summary to get cleaner output for parsing
	command := []string{"clamscan", "--no-summary", "{FILE_PATH}"}

	stdout, stderr, err := sb.RunInSandbox(ctx, c.clamscanImage, filePath, command)
	if err != nil {
		c.logger.Errorf("ClamAV sandbox execution failed for file %s: %v, stderr: %s", filePath, err, stderr)
		return ScanResult{Result: "error", Details: fmt.Sprintf("ClamAV sandbox execution failed: %v", err)}, err
	}

	c.logger.Debugf("ClamAV sandbox stdout: %s", stdout)
	c.logger.Debugf("ClamAV sandbox stderr: %s", stderr)

	// Parse stdout from clamscan
	// Example output: /scan_target/test.txt: EICAR-Test-File(Virus-Test-File) FOUND
	// Example output: /scan_target/clean.txt: OK
	if strings.Contains(stdout, "FOUND") {
		// Extract threat name if possible
		parts := strings.Split(stdout, ":")
		threat := "Unknown Threat"
		if len(parts) > 1 {
			threat = strings.TrimSpace(strings.Split(parts[1], " FOUND")[0])
		}
		return ScanResult{Result: "malicious", Details: fmt.Sprintf("Threat found: %s", threat), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	} else if strings.Contains(stdout, "OK") {
		return ScanResult{Result: "clean", Details: "No threats found", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	} else if strings.Contains(stderr, "ERROR") || strings.Contains(stdout, "ERROR") {
		return ScanResult{Result: "error", Details: fmt.Sprintf("ClamAV internal error: %s", stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("clamav internal error: %s", stderr)
	}

	return ScanResult{Result: "error", Details: fmt.Sprintf("Unknown ClamAV sandbox response: %s", stdout), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("unknown clamav sandbox response")
}
