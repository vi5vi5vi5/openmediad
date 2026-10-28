package initd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Страж проверяется настоящим sh с поддельными iptables и openmediad: правило iptables хранится в файле, поддельный
// демон ведёт себя по файлу mode (live — работает до SIGTERM, crash — сразу падает, exit0 — сам выходит с кодом 0).

const fakeIptables = `#!/bin/sh
R="$FAKE/rules"
touch "$R"
shift 2 # -t nat
op=$1; shift 2 # -S/-I/-D OUTPUT
case $op in
-S) cat "$R" ;;
-I) shift; echo "$*" >> "$R" ;;
-D) grep -qxF -- "$*" "$R" || exit 1
    awk -v r="$*" 'BEGIN{d=0} $0==r && !d {d=1; next} {print}' "$R" > "$R.new"; mv "$R.new" "$R" ;;
esac
`

const fakeDaemon = `#!/bin/sh
case $1 in
config) echo "${MAX:-3}" ;;
reroute) echo reroute >> "$FAKE/reroute" ;;
run)
	echo $$ >> "$FAKE/started"
	case $(cat "$FAKE/mode") in
	crash) exit 1 ;;
	exit0) sleep 0.3; exit 0 ;;
	esac
	trap 'echo term >> "$FAKE/terms"; exit 0' TERM
	while :; do sleep 0.1; done ;;
esac
`

type fake struct {
	t     *testing.T
	dir   string
	guard string
	env   []string
}

func newFake(t *testing.T, mode string, max int) *fake {
	if runtime.GOOS != "linux" {
		t.Skip("страж проверяется только на Linux")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("нет sh")
	}
	dir := t.TempDir()
	for _, d := range []string{"path", "bin", "run", "state"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	// На колонке страж работает в busybox: если он есть на компьютере, проверяем под ним и с его утилитами.
	if bb, err := exec.LookPath("busybox"); err == nil && os.Getenv("OMD_GUARD_SH") != "sh" {
		for _, a := range []string{"sh", "tr", "grep", "date", "wc", "mv", "cat", "nice", "sleep", "rm", "mkdir", "kill"} {
			os.Symlink(bb, filepath.Join(dir, "path", a))
		}
	}
	os.WriteFile(filepath.Join(dir, "path", "iptables"), []byte(fakeIptables), 0o755)
	bin := filepath.Join(dir, "bin", "openmediad")
	os.WriteFile(bin, []byte(fakeDaemon), 0o755)
	os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o644)
	guard := filepath.Join(dir, "guard.sh")
	os.WriteFile(guard, []byte(Guard(GuardPaths{Bin: bin, Run: filepath.Join(dir, "run"), State: filepath.Join(dir, "state")})), 0o644)
	env := append(os.Environ(), "FAKE="+dir, "MAX="+strconv.Itoa(max), "PATH="+filepath.Join(dir, "path")+":"+os.Getenv("PATH"))
	return &fake{t: t, dir: dir, guard: guard, env: env}
}

// start запускает стража в фоне; возвращает процесс.
func (f *fake) start(mode string) *exec.Cmd {
	cmd := exec.Command(f.sh(), f.guard, mode)
	cmd.Env = f.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		f.run("stop")
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd
}

// sh — оболочка стража: busybox, если подложен в PATH подделок, иначе системная.
func (f *fake) sh() string {
	if p := filepath.Join(f.dir, "path", "sh"); fileExists(p) {
		return p
	}
	return "sh"
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (f *fake) run(mode string) string {
	cmd := exec.Command(f.sh(), f.guard, mode)
	cmd.Env = f.env
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func (f *fake) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return strings.TrimSpace(string(b))
}

func (f *fake) rules() int {
	r := f.read("rules")
	if r == "" {
		return 0
	}
	return len(strings.Split(r, "\n"))
}

// waitFor ждёт условия до d.
func (f *fake) waitFor(what string, d time.Duration, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("не дождались: %s (state=%q, rules=%d, log:\n%s)", what, f.read("run/guard.state"), f.rules(), f.read("run/guard.log"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestGuardStartStop(t *testing.T) {
	f := newFake(t, "live", 3)
	g := f.start("start")
	f.waitFor("демон запущен и правило стоит", 3*time.Second, func() bool {
		return strings.HasPrefix(f.read("run/guard.state"), "running") && f.rules() == 1
	})
	if f.read("reroute") != "" {
		t.Error("при ручном запуске reroute делает сама команда start, страж не должен")
	}
	if out := f.run("start"); !strings.Contains(out, "уже работает") {
		t.Errorf("второй страж: %q", out)
	}
	f.run("stop")
	if f.rules() != 0 {
		t.Error("после stop правило осталось")
	}
	if f.read("terms") != "term" {
		t.Errorf("демон не получил SIGTERM: %q", f.read("terms"))
	}
	if st := f.read("run/guard.state"); st != "stopped" {
		t.Errorf("state=%q", st)
	}
	done := make(chan struct{})
	go func() { g.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("страж не вышел после stop")
	}
}

func TestGuardRemovesRuleWhenDaemonKilledAndRestarts(t *testing.T) {
	f := newFake(t, "live", 3)
	f.start("start")
	f.waitFor("первый запуск", 3*time.Second, func() bool { return f.rules() == 1 && f.read("run/daemon.pid") != "" })
	pid, _ := strconv.Atoi(f.read("run/daemon.pid"))
	killed := time.Now()
	syscall.Kill(pid, syscall.SIGKILL)
	f.waitFor("правило снято после kill -9", 2*time.Second, func() bool { return f.rules() == 0 })
	if d := time.Since(killed); d > 2*time.Second {
		t.Errorf("правило снято через %v, норма ≤2 с", d)
	}
	f.waitFor("перезапуск", 5*time.Second, func() bool {
		return strings.Count(f.read("started"), "\n") == 1 && f.rules() == 1
	})
	f.waitFor("reroute после падения", 2*time.Second, func() bool { return f.read("reroute") == "reroute" })
	if c := f.read("run/crashes"); c != "1" {
		t.Errorf("счётчик падений %q, ждали 1", c)
	}
}

func TestGuardGivesUpAfterMaxCrashes(t *testing.T) {
	f := newFake(t, "crash", 2)
	g := f.start("start")
	done := make(chan struct{})
	go func() { g.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("страж не сдался (log:\n%s)", f.read("run/guard.log"))
	}
	if st := f.read("run/guard.state"); st != "gave-up 2" {
		t.Errorf("state=%q", st)
	}
	if f.rules() != 0 {
		t.Error("страж сдался, а правило осталось")
	}
	if n := strings.Count(f.read("started"), "\n") + 1; n != 2 {
		t.Errorf("запусков %d, ждали 2", n)
	}
	// ручной start обнуляет счётчик
	os.WriteFile(filepath.Join(f.dir, "mode"), []byte("live"), 0o644)
	f.start("start")
	f.waitFor("запуск после ручного start", 3*time.Second, func() bool {
		return strings.HasPrefix(f.read("run/guard.state"), "running")
	})
}

func TestGuardDaemonExitZeroStopsGuard(t *testing.T) {
	f := newFake(t, "exit0", 3)
	g := f.start("start")
	done := make(chan struct{})
	go func() { g.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("страж не вышел, когда демон завершился с кодом 0")
	}
	if f.rules() != 0 || f.read("run/guard.state") != "stopped" {
		t.Errorf("rules=%d state=%q", f.rules(), f.read("run/guard.state"))
	}
}

func TestGuardBootRespectsAutostartOff(t *testing.T) {
	f := newFake(t, "live", 3)
	os.WriteFile(filepath.Join(f.dir, "state", "autostart-off"), nil, 0o644)
	f.run("boot")
	if st := f.read("run/guard.state"); st != "disabled" || f.read("started") != "" {
		t.Errorf("state=%q, started=%q", st, f.read("started"))
	}
}

func TestGuardBootReroutesFirstStart(t *testing.T) {
	f := newFake(t, "live", 3)
	f.start("boot")
	f.waitFor("reroute при загрузке", 3*time.Second, func() bool { return f.read("reroute") == "reroute" })
}

func TestInitScript(t *testing.T) {
	s := Init("/data/opt/openmediad")
	if !strings.Contains(s, "D=/data/opt/openmediad") || !strings.Contains(s, `guard.sh" boot`) || strings.Contains(s, "@") {
		t.Fatalf("скрипт автозапуска:\n%s", s)
	}
	if strings.Contains(Guard(GuardPaths{Bin: "/b", Run: "/r", State: "/s"}), "@") {
		t.Fatal("в страже остались неподставленные @…@")
	}
}
