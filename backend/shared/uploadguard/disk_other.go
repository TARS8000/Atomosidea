//go:build !linux && !darwin

package uploadguard

import "errors"

// FreeDiskBytes はこの OS では空き容量を読まない（判定はせずに通す）。本番は Linux のコンテナで動く。
func FreeDiskBytes(path string) (int64, error) {
	return 0, errors.New("free disk space is not supported on this OS")
}
