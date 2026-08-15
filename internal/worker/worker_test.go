package worker

import (
	"testing"

	"github.com/atmosidea/sfsp/internal/scanner"
	"github.com/stretchr/testify/assert"
)

func TestDetermineOverallStatus(t *testing.T) {
	testCases := []struct {
		name         string
		clamavResult string
		yaraResult   string
		expected     string
	}{
		{"Both Clean", "clean", "clean", "clean"},
		{"ClamAV Malicious", "malicious", "clean", "malicious"},
		{"YARA Suspicious", "clean", "suspicious", "suspicious"},
		{"Both Malicious/Suspicious", "malicious", "suspicious", "malicious"},
		{"ClamAV Error", "error", "clean", "failed"},
		{"YARA Error", "clean", "error", "failed"},
		{"Both Error", "error", "error", "failed"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clamavRes := scanner.ScanResult{Result: tc.clamavResult}
			yaraRes := scanner.ScanResult{Result: tc.yaraResult}
			status := determineOverallStatus(clamavRes, yaraRes)
			assert.Equal(t, tc.expected, status)
		})
	}
}
