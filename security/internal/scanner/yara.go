package scanner

import (
	"context"
	"fmt"
	"strings"

	"github.com/atmosidea/sfsp/internal/sandbox"
	"go.uber.org/zap"
)

// YARAScanner implements the Scanner interface for YARA
type YARAScanner struct {
	yaraImage string // Docker image for yara client
	rulesPath string // Path to YARA rules file on the worker's filesystem
	logger    *zap.SugaredLogger
}

// NewYARAScanner creates a new YARAScanner instance
func NewYARAScanner(yaraImage, rulesPath string, logger *zap.SugaredLogger) *YARAScanner {
	return &YARAScanner{
		yaraImage: yaraImage,
		rulesPath: rulesPath,
		logger:    logger,
	}
}

// Scan performs a YARA scan on the given file path using the provided sandbox.
func (y *YARAScanner) Scan(ctx context.Context, sb sandbox.Sandbox, jobID string, filePath string) (ScanResult, error) {
	// Add -r for recursive scan of directories
	command := []string{"yara", "-r", "-w", "-m", y.rulesPath, "{FILE_PATH}"}

	stdout, stderr, err := sb.RunInSandbox(ctx, jobID, y.yaraImage, filePath, command)
	if err != nil {
		y.logger.Errorf("YARA sandbox execution failed for file %s: %v, stderr: %s", filePath, err, stderr)
		return ScanResult{Result: "error", Details: fmt.Sprintf("YARA sandbox execution failed: %v", err), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, err
	}

	y.logger.Infof("YARA sandbox stdout: %s", stdout) // Changed to Infof for debugging
	y.logger.Infof("YARA sandbox stderr: %s", stderr) // Changed to Infof for debugging

	// Parse line-based YARA output
	// Example output: RULE_NAME /path/to/file
	matches := []string{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A match line typically starts with the rule name
		if !strings.HasPrefix(line, "error:") && !strings.HasPrefix(line, "warning:") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				matches = append(matches, parts[0]) // Extract rule name
			}
		}
	}

	if len(matches) > 0 {
		details := fmt.Sprintf("Matched rules: %s", strings.Join(matches, ", "))
		return ScanResult{Result: "suspicious", Details: details, RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	}

	if strings.Contains(stderr, "error:") || strings.Contains(stdout, "error:") {
		return ScanResult{Result: "error", Details: fmt.Sprintf("YARA execution error: %s", stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("yara execution error")
	}

	return ScanResult{Result: "clean", Details: "No YARA matches", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
}
