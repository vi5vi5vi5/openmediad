//go:build linux

package aplay

import "syscall"

// setPipeSize меняет размер трубы (F_SETPIPE_SZ). Не вышло — остаётся системный размер: это только задержка.
func setPipeSize(w any, n int) {
	sc, ok := w.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return
	}
	rc.Control(func(fd uintptr) {
		const fSetPipeSz = 1031
		syscall.Syscall(syscall.SYS_FCNTL, fd, fSetPipeSz, uintptr(n))
	})
}
