package disk

import (
	"os"
	"path/filepath"
	"strings"

	"openmediad/internal/core/platform/proc"
)

// ReadOnly — смонтирован ли раздел с точкой монтирования mnt только для чтения (по /proc/mounts).
func ReadOnly(mnt string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(proc.Root, "mounts"))
	if err != nil {
		return false, err
	}
	ro := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != mnt {
			continue
		}
		// у / бывает несколько строк (rootfs и настоящий раздел): действует последняя
		ro = false
		for _, o := range strings.Split(f[3], ",") {
			if o == "ro" {
				ro = true
			}
		}
	}
	return ro, nil
}
