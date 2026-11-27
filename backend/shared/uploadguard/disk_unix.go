//go:build linux || darwin

package uploadguard

import "syscall"

// FreeDiskBytes は path のあるファイルシステムで、一般ユーザーが使える空き容量を返す。
// コンテナの中から呼ぶと、Docker のデータ領域（VM のディスク）の空きになる。
func FreeDiskBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
