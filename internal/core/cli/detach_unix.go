//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach отделяет процесс от терминала: страж переживает выход из ssh.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
