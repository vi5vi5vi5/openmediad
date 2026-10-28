package install

import (
	"fmt"
	"os/exec"
	"strings"

	"openmediad/internal/system/disk"
)

// Rootfs — правка корня колонки: он смонтирован только для чтения, поэтому на время правки — remount rw, после — sync
// и прежний режим. Колонка не остаётся с корнем в rw, даже если правка не удалась.
type Rootfs struct {
	Remount func(rw bool) error // по умолчанию mount -o remount,rw|ro /
	Sync    func()              // по умолчанию sync
}

// Edit выполняет fn с корнем, доступным для записи.
func (r *Rootfs) Edit(fn func() error) error {
	ro, err := disk.ReadOnly("/")
	if err != nil {
		ro = true // не знаем — считаем, что только для чтения (remount rw безвреден)
	}
	if ro {
		if err := r.remount(true); err != nil {
			return fmt.Errorf("mount -o remount,rw /: %w", err)
		}
	}
	err = fn()
	r.sync()
	if ro {
		if e := r.remount(false); e != nil && err == nil {
			err = fmt.Errorf("mount -o remount,ro /: %w", e)
		}
	}
	return err
}

func (r *Rootfs) remount(rw bool) error {
	if r.Remount != nil {
		return r.Remount(rw)
	}
	mode := "remount,ro"
	if rw {
		mode = "remount,rw"
	}
	out, err := exec.Command("mount", "-o", mode, "/").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Rootfs) sync() {
	if r.Sync != nil {
		r.Sync()
		return
	}
	exec.Command("sync").Run()
}
