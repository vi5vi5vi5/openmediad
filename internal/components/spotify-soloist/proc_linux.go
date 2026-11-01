//go:build linux

package spotifysoloist

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// sysProcAttr: свой процессный группа (kill накрывает потомков) и SIGKILL Soloist, если создавший его поток демона умер.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// lockThread привязывает горутину надзора к потоку ОС: Pdeathsig срабатывает при смерти именно этого потока.
func lockThread() { runtime.LockOSThread() }

// setNice задаёт приоритет процесса (демон работает с повышенным, Soloist ему не нужен).
func setNice(pid, nice int) error { return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice) }

// threadTicks — процессорное время (user+sys, в тиках) каждого потока процесса по /proc/<pid>/task/*/stat.
func threadTicks(pid int) (map[int]uint64, error) {
	dirs, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/stat", pid))
	if err != nil || len(dirs) == 0 {
		return nil, fmt.Errorf("нет потоков процесса %d", pid)
	}
	out := make(map[int]uint64, len(dirs))
	for _, f := range dirs {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // поток успел завершиться
		}
		// имя потока в скобках может содержать пробелы: поля считаем после последней «)»
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(b[i+1:]))
		if len(fields) < 13 { // state — поле 3; utime — 14-е, stime — 15-е
			continue
		}
		ut, e1 := strconv.ParseUint(fields[11], 10, 64)
		st, e2 := strconv.ParseUint(fields[12], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		tid, err := strconv.Atoi(filepath.Base(filepath.Dir(f)))
		if err != nil {
			continue
		}
		out[tid] = ut + st
	}
	return out, nil
}

// killOrphan гасит Soloist, оставшийся от прошлого запуска демона (pid из файла; процесс должен быть нашим Soloist).
func killOrphan(pidFile, soloistPath string) (killed bool) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	os.Remove(pidFile)
	if err != nil || pid <= 1 {
		return false
	}
	cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !bytes.Contains(cmd, []byte(soloistPath)) {
		return false
	}
	syscall.Kill(-pid, syscall.SIGKILL) // группа: у нас Setpgid
	syscall.Kill(pid, syscall.SIGKILL)
	return true
}
