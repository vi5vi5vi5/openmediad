package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProc строит каталог в виде /proc с процессами pid → (ppid, comm, cmdline).
func fakeProc(t *testing.T, ps map[int][3]string) {
	t.Helper()
	dir := t.TempDir()
	for pid, p := range ps {
		d := filepath.Join(dir, strconv.Itoa(pid))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "stat"), []byte(strconv.Itoa(pid)+" ("+p[1]+") S "+p[0]+" 1 1 0"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(p[2]), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "meminfo"), []byte("MemTotal: 1000 kB\nMemAvailable:     4242 kB\n"), 0o644)
	old := Root
	Root = dir
	t.Cleanup(func() { Root = old })
}

func TestListAndDescendants(t *testing.T) {
	fakeProc(t, map[int][3]string{
		1:   {"0", "init", "/sbin/init\x00"},
		100: {"1", "openmediad", "/data/opt/openmediad/bin/openmediad\x00run\x00"},
		101: {"100", "aplay", "/usr/bin/aplay\x00-q\x00"},
		102: {"100", "maind", "/system/vendor/quasar/maind\x00--gst-launch\x00"},
		103: {"102", "weird) name", "x\x00"},
		200: {"1", "maind", "/system/vendor/quasar/maind\x00--mediad\x00"},
	})
	ps, err := List()
	if err != nil || len(ps) != 6 {
		t.Fatalf("List: %v, %d процессов", err, len(ps))
	}
	d := Descendants(ps, 100)
	if len(d) != 3 {
		t.Fatalf("потомков %d, ждали 3: %+v", len(d), d)
	}
	m := Find(ps, func(p Process) bool { return p.Exe() == "maind" && p.HasArg("--mediad") })
	if len(m) != 1 || m[0].PID != 200 {
		t.Fatalf("mediad: %+v", m)
	}
	p, ok := Get(103)
	if !ok || p.Comm != "weird) name" || p.PPID != 102 {
		t.Fatalf("имя со скобкой: %+v", p)
	}
	if kb, err := MemAvailableKB(); err != nil || kb != 4242 {
		t.Fatalf("MemAvailable: %d %v", kb, err)
	}
}

func TestPssFromSmaps(t *testing.T) {
	fakeProc(t, map[int][3]string{5: {"1", "x", "x\x00"}})
	os.WriteFile(filepath.Join(Root, "5", "smaps"), []byte("00400000-00452000 r-xp\nRss:  100 kB\nPss:   40 kB\nffff-ffff rw-p\nPss:  2 kB\n"), 0o644)
	if kb, err := PssKB(5); err != nil || kb != 42 {
		t.Fatalf("PssKB = %d, %v; ждали 42", kb, err)
	}
	if !Alive(5) || Alive(6) {
		t.Fatal("Alive")
	}
}
