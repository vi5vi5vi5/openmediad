//go:build !unix

package cli

import "os/exec"

func detach(cmd *exec.Cmd) {}
