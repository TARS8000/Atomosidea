// Package sandbox は信頼できないファイルを隔離環境（Docker コンテナ）内でスキャンするための実行基盤を提供する。
package sandbox

import (
	"context"
	"errors"
	"fmt"
)

// ExitError はサンドボックス内のコマンドが 0 以外の終了コードで終了したときに RunInSandbox が返すエラー。
// この場合も stdout/stderr は併せて返される。
type ExitError struct {
	Code int64
}

// Error は終了コードを含むエラーメッセージを返す。
func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox command exited with status %d", e.Code)
}

// ExitCode は err が保持する終了コードを返す。err が ExitError でなければ -1 を返す。
func ExitCode(err error) int64 {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return -1
}

// Sandbox は隔離環境でコマンドを実行するためのインターフェース。
// command 中の {FILE_PATH} は、実装側で隔離環境内から見た filePathToScan のパスに置き換えられる。
type Sandbox interface {
	RunInSandbox(ctx context.Context, jobID string, image string, filePathToScan string, command []string) (stdout string, stderr string, err error)
}

