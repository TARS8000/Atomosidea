// Package scanner はサンドボックス内で ClamAV / YARA を実行し、結果を判定するスキャナーを提供する。
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

// ClamAVLimits は clamscan に明示的に渡す上限値。clamscan の既定値は小さく（例: 最大ファイルサイズ 100 MB）、
// 指定しないと上限を超えたファイルは検査されずに clean と報告されてしまうため。
// 0 以下の項目はオプションを付けない（clamscan の既定値になる）。
type ClamAVLimits struct {
	MaxFileSize     int64 // バイト
	MaxScanSize     int64 // バイト
	MaxFiles        int
	MaxRecursion    int
	MaxDirRecursion int
	MaxScanTimeMs   int64
	PCREMaxFileSize int64 // バイト
}

// ClamAVScanner は ClamAV（clamscan）をサンドボックス内で実行するスキャナー。
type ClamAVScanner struct {
	clamscanImage string // clamscan クライアントの Docker イメージ
	limits        ClamAVLimits
	logger        *zap.SugaredLogger
}

// NewClamAVScanner は新しい ClamAVScanner を作成する。
func NewClamAVScanner(clamscanImage string, limits ClamAVLimits, logger *zap.SugaredLogger) *ClamAVScanner {
	return &ClamAVScanner{
		clamscanImage: clamscanImage,
		limits:        limits,
		logger:        logger,
	}
}

// command はサンドボックスで実行する clamscan のコマンドラインを組み立てる。
// 検査対象パスは {FILE_PATH} のプレースホルダーとして末尾に置き、サンドボックス側で置換される。
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

// parseFound は "<パス>: <検出名> FOUND" 形式のすべての行から検出名（シグネチャ名）を取り出して返す。
// パスに ": " が含まれる場合に備え、最後の ": " で区切る。
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

// Scan は指定されたサンドボックスを使って filePath を ClamAV でスキャンする。
// clamscan の終了コード: 0 = ウイルスなし、1 = ウイルス検出、それ以外 = エラー。
// 判定は終了コードで行い、出力中の "OK" などの文字列では判定しない。これにより一部のエラーを
// clean と誤認することを防ぐ。clean になるのは終了コード 0 の場合だけである。
func (c *ClamAVScanner) Scan(ctx context.Context, sb sandbox.Sandbox, jobID string, filePath string) (ScanResult, error) {
	// 1. サンドボックスで clamscan を実行する
	command := c.command()

	stdout, stderr, err := sb.RunInSandbox(ctx, jobID, c.clamscanImage, filePath, command)
	rawOutput := &map[string]any{"stdout": stdout, "stderr": stderr}

	// 2. 終了コード 0 のときだけ clean とする
	if err == nil {
		c.logger.Infof("ClamAV scan clean for %s", filePath)
		return ScanResult{Result: "clean", Details: "No threats found", RawOutput: rawOutput}, nil
	}

	// 3. 検出名を、本物の検出と上限超過（Heuristics.Limits.Exceeded.*）に分ける
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
