//go:build unix

package initd

import (
	"os/exec"
	"syscall"
)

// detach отделяет стража от группы процессов теста (как openmediad start на колонке).
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// killHard — kill -9: так проверяется, что страж снимает правило после гибели демона.
func killHard(pid int) { syscall.Kill(pid, syscall.SIGKILL) }
