package scanner

import (
	"context"
	"fmt"
	"strings"

	"github.com/atmosidea/sfsp/internal/sandbox"
	"go.uber.org/zap"
)

// YARAScanner はサンドボックスのコンテナ内で YARA を実行するスキャナー。
type YARAScanner struct {
	yaraImage string // yara クライアントの Docker イメージ
	rulesPath string // yara-client イメージ内の YARA ルールファイルのパス（ワーカー自身は読まない）
	logger    *zap.SugaredLogger
}

// NewYARAScanner は新しい YARAScanner を作成する。
func NewYARAScanner(yaraImage, rulesPath string, logger *zap.SugaredLogger) *YARAScanner {
	return &YARAScanner{
		yaraImage: yaraImage,
		rulesPath: rulesPath,
		logger:    logger,
	}
}

// Scan は指定されたサンドボックスを使って filePath を YARA でスキャンする。
// いずれかのルールに一致すれば suspicious、実行に失敗またはエラー出力があれば error、
// どちらでもなければ clean を返す（YARA の一致だけでは malicious にはしない）。
func (y *YARAScanner) Scan(ctx context.Context, sb sandbox.Sandbox, jobID string, filePath string) (ScanResult, error) {
	// ディレクトリを再帰的に検査するため -r を付ける（-w は警告を抑止、-m はメタデータも出力）
	command := []string{"yara", "-r", "-w", "-m", y.rulesPath, "{FILE_PATH}"}

	// 1. サンドボックスで yara を実行する。終了コードが 0 以外なら error とする
	stdout, stderr, err := sb.RunInSandbox(ctx, jobID, y.yaraImage, filePath, command)
	if err != nil {
		y.logger.Errorf("YARA sandbox execution failed for file %s: %v, stderr: %s", filePath, err, stderr)
		return ScanResult{Result: "error", Details: fmt.Sprintf("YARA sandbox execution failed: %v", err), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, err
	}

	y.logger.Infof("YARA sandbox stdout: %s", stdout) // デバッグ用に Infof に変更
	y.logger.Infof("YARA sandbox stderr: %s", stderr) // デバッグ用に Infof に変更

	// 2. 行単位の YARA 出力を解析する
	// 出力例: RULE_NAME /path/to/file
	matches := []string{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 一致を示す行は通常ルール名で始まる
		if !strings.HasPrefix(line, "error:") && !strings.HasPrefix(line, "warning:") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				matches = append(matches, parts[0]) // ルール名を取り出す
			}
		}
	}

	// 3. 一致・エラー・clean の順に判定する
	if len(matches) > 0 {
		details := fmt.Sprintf("Matched rules: %s", strings.Join(matches, ", "))
		return ScanResult{Result: "suspicious", Details: details, RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
	}

	if strings.Contains(stderr, "error:") || strings.Contains(stdout, "error:") {
		return ScanResult{Result: "error", Details: fmt.Sprintf("YARA execution error: %s", stderr), RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, fmt.Errorf("yara execution error")
	}

	return ScanResult{Result: "clean", Details: "No YARA matches", RawOutput: &map[string]any{"stdout": stdout, "stderr": stderr}}, nil
}
