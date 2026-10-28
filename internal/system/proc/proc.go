// Package proc — сведения о процессах из /proc: кто запущен, кто чей потомок, сколько памяти занимает.
//
// Нужен CLI (status, start, stop, doctor): найти штатный mediad, посчитать память демона вместе с его aplay и декодерами.
// Только чтение; на компьютере без /proc функции возвращают пустые результаты или ошибку.
package proc

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root — где смонтирован procfs (подменяется в тестах).
var Root = "/proc"

// Process — один процесс.
type Process struct {
	PID  int
	PPID int
	Comm string   // имя из /proc/PID/stat (до 15 символов)
	Args []string // командная строка; пусто у потоков ядра
}

// List — все процессы. Исчезнувшие во время обхода пропускаются.
func List() ([]Process, error) {
	ents, err := os.ReadDir(Root)
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, ok := Get(pid); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// Get читает один процесс.
func Get(pid int) (Process, bool) {
	dir := filepath.Join(Root, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Process{}, false
	}
	p, ok := parseStat(stat)
	if !ok {
		return Process{}, false
	}
	if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		p.Args = splitCmdline(cmd)
	}
	return p, true
}

// parseStat разбирает /proc/PID/stat: «pid (comm) state ppid …». Имя в скобках может содержать пробелы и скобки.
func parseStat(b []byte) (Process, bool) {
	open := bytes.IndexByte(b, '(')
	end := bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return Process{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:open])))
	if err != nil {
		return Process{}, false
	}
	rest := strings.Fields(string(b[end+1:]))
	if len(rest) < 2 {
		return Process{}, false
	}
	ppid, _ := strconv.Atoi(rest[1])
	return Process{PID: pid, PPID: ppid, Comm: string(b[open+1 : end])}, true
}

func splitCmdline(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	return strings.Split(string(b), "\x00")
}

// Alive — существует ли процесс.
func Alive(pid int) bool {
	_, err := os.Stat(filepath.Join(Root, strconv.Itoa(pid)))
	return pid > 0 && err == nil
}

// Descendants — все потомки процесса (дети, внуки…) из списка ps.
func Descendants(ps []Process, pid int) []Process {
	var out []Process
	queue := []int{pid}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, p := range ps {
			if p.PPID == parent && p.PID != pid {
				out = append(out, p)
				queue = append(queue, p.PID)
			}
		}
	}
	return out
}

// Find — процессы, у которых match(p) истинно.
func Find(ps []Process, match func(Process) bool) []Process {
	var out []Process
	for _, p := range ps {
		if match(p) {
			out = append(out, p)
		}
	}
	return out
}

// HasArg — есть ли среди аргументов процесса a.
func (p Process) HasArg(a string) bool {
	for _, s := range p.Args {
		if s == a {
			return true
		}
	}
	return false
}

// Exe — первое слово командной строки без пути (или имя из stat у потоков ядра).
func (p Process) Exe() string {
	if len(p.Args) == 0 {
		return p.Comm
	}
	return filepath.Base(p.Args[0])
}

// PssKB — пропорциональная доля памяти процесса в КБ (общие страницы делятся между процессами, которые их держат):
// честная цена процесса для колонки. Читает smaps_rollup, а на старых ядрах — сумму по smaps.
func PssKB(pid int) (int64, error) {
	dir := filepath.Join(Root, strconv.Itoa(pid))
	f, err := os.Open(filepath.Join(dir, "smaps_rollup"))
	if err != nil {
		if f, err = os.Open(filepath.Join(dir, "smaps")); err != nil {
			return 0, err
		}
	}
	defer f.Close()
	var sum int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("Pss:")) {
			continue
		}
		fs := strings.Fields(string(line[4:]))
		if len(fs) > 0 {
			n, _ := strconv.ParseInt(fs[0], 10, 64)
			sum += n
		}
	}
	return sum, sc.Err()
}

// MemAvailableKB — MemAvailable из /proc/meminfo (сколько памяти система может отдать без вытеснения).
func MemAvailableKB() (int64, error) {
	b, err := os.ReadFile(filepath.Join(Root, "meminfo"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			fs := strings.Fields(rest)
			if len(fs) > 0 {
				return strconv.ParseInt(fs[0], 10, 64)
			}
		}
	}
	return 0, os.ErrNotExist
}
