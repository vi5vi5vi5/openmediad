//go:build !unix

package initd

import "os/exec"

// Страж проверяется только на Linux (newFake пропускает тест), но файл должен собираться и здесь.
func detach(cmd *exec.Cmd) {}

func killHard(pid int) {}
