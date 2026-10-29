//go:build unix

// Package disk — свободное место на разделе и режим монтирования корня.
package disk

import "syscall"

// FreeKB — сколько КБ свободно на разделе с путём path (для обычного пользователя).
func FreeKB(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize) / 1024, nil
}
