package scanner

import (
	"context"
	"encoding/json"
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
// It now takes the image name for the yara client that will run in the sandbox.
func NewYARAScanner(yaraImage, rulesPath string, logger *zap.SugaredLogger) *YARAScanner {
	return &YARAScanner{
		yaraImage: yaraImage,
		rulesPath: rulesPath,
		logger:    logger,
	}
}

// Scan performs a YARA scan on the given file path using the provided sandbox.
func (y *YARAScanner) Scan(ctx context.Context, sb sandbox.Sandbox, filePath string) (ScanResult, error) {
	// The command to execute inside the sandbox container
	// {FILE_PATH} will be replaced by the sandbox implementation with the actual mounted path
	// -r: recursively scan directories (though we scan a single file)
	// -w: disable warnings
	// -m: print metadata
	// --json: output results in JSON format
	// The YARA rules file will be mounted into the sandbox from the worker's filesystem.
	// Assuming rulesPath is /etc/yara-rules/general.yar on worker, it will be mounted to /etc/yara-rules in sandbox.
	// So, the command inside sandbox will refer to /etc/yara-rules/general.yar
	command := []string{"yara", "-r", "-w", "-m", "--json", y.rulesPath, "{FILE_PATH}"}

	stdout, stderr, err := sb.RunInSandbox(ctx, y.yaraImage, filePath, command)
	if err != nil {
		y.logger.Errorf("YARA sandbox execution failed for file %s: %v, stderr: %s", filePath, err, stderr)
		return ScanResult{Result: "error", Details: fmt.Sprintf("YARA sandbox execution failed: %v", err)}, err
	}

	y.logger.Debugf("YARA sandbox stdout: %s", stdout)
	y.logger.Debugf("YARA sandbox stderr: %s", stderr)

	// Parse JSON output from YARA
	var yaraMatches []struct {
		Rule      string `json:"rule"`
		Namespace string `json:"namespace"`
		Tags      []string `json:"tags"`
		Matches   []struct {
			Offset int    `json:"offset"`
			Data   string `json:"data"`
		} `json:"matches"`
		Strings []struct {
			Name   string `json:"name"`
			Offset int    `json:"offset"`
			Data   string `json:"data"`
		} `json:"strings"`
	}

	// YARA's --json output can sometimes have non-JSON lines before the actual JSON,
	// especially if there are warnings or errors. We need to find the actual JSON part.
	jsonStart := strings.Index(stdout, "[")
	jsonEnd := strings.LastIndex(stdout, "]")
	if jsonStart == -1 || jsonEnd == -1 || jsonStart >= jsonEnd {
		// No JSON output, or invalid JSON structure. This might mean no matches or an error.
		if strings.Contains(stderr, "error:") || strings.Contains(stdout, "error:") {
			y.logger.Errorf("YARA scan error for file %s: %s", filePath, stderr)
			return ScanResult{Result: "error", Details: fmt.Sprintf("YARA execution error: %s", stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("yara execution error")
		}
		// If no error, assume no matches
		return ScanResult{Result: "clean", Details: "No YARA matches", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	}

	jsonOutput := stdout[jsonStart : jsonEnd+1]
	if err := json.Unmarshal([]byte(jsonOutput), &yaraMatches); err != nil {
		y.logger.Errorf("Failed to parse YARA JSON output for file %s: %v, raw output: %s", filePath, err, jsonOutput)
		return ScanResult{Result: "error", Details: fmt.Sprintf("Failed to parse YARA output: %v", err), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, err
	}

	if len(yaraMatches) > 0 {
		details := fmt.Sprintf("Matched rules: %s", yaraMatches[0].Rule) // Just take the first rule for details
		rawOutputMap := make(map[string]any)
		if err := json.Unmarshal([]byte(jsonOutput), &rawOutputMap); err != nil {
			y.logger.Warnf("Failed to unmarshal YARA raw output to map: %v", err)
		}
		return ScanResult{Result: "suspicious", Details: details, RawOutput: &rawOutputMap}, nil
	}

	return ScanResult{Result: "clean", Details: "No YARA matches", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
}
