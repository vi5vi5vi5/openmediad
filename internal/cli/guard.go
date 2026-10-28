package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openmediad/internal/config"
	"openmediad/internal/control"
	"openmediad/internal/system/device"
	"openmediad/internal/system/initd"
	"openmediad/internal/system/layout"
	"openmediad/internal/system/proc"
)

// requireSpeaker — команда меняет систему: только на колонке и от root.
func requireSpeaker() error {
	if err := device.Check(); err != nil {
		return fmt.Errorf("%w (%v)", ErrNotDevice, err)
	}
	return device.RequireRoot()
}

// guardPaths — что подставить в страж: установленный бинарник и постоянные отметки на /data, а без установки — этот
// бинарник и отметки в ОЗУ.
func guardPaths() (initd.GuardPaths, error) {
	if layout.Installed() {
		return initd.GuardPaths{Bin: layout.Bin(), Run: layout.Run(), State: layout.State()}, nil
	}
	self, err := os.Executable()
	if err != nil {
		return initd.GuardPaths{}, err
	}
	self, _ = filepath.EvalSymlinks(self)
	return initd.GuardPaths{Bin: self, Run: layout.Run(), State: filepath.Join(layout.Run(), "state")}, nil
}

// writeGuard пишет страж из шаблона этого бинарника (если текст отличается) и возвращает путь.
func writeGuard() (string, error) {
	p, err := guardPaths()
	if err != nil {
		return "", err
	}
	path := layout.Guard()
	text := initd.Guard(p)
	if old, err := os.ReadFile(path); err == nil && string(old) == text {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".new", []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(path+".new", path)
}

// launchGuard запускает стража в фоне, отдельно от терминала.
func launchGuard(path, mode string) error {
	cmd := exec.Command("sh", path, mode)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runGuard выполняет действие стража и ждёт его (stop).
func runGuard(c *Ctx, path, mode string) error {
	cmd := exec.Command("sh", path, mode)
	cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
	return cmd.Run()
}

// guardRunning — pid живого стража (0 — нет).
func guardRunning() int {
	return livePID(layout.GuardPID(), func(p proc.Process) bool { return strings.Contains(strings.Join(p.Args, " "), "guard.sh") })
}

// daemonPID — pid живого демона по файлу стража (0 — нет).
func daemonPID() int {
	return livePID(layout.DaemonPID(), func(p proc.Process) bool { return p.HasArg("run") })
}

// livePID — pid из файла, если процесс жив и это он (pid мог достаться чужому процессу).
func livePID(file string, ours func(proc.Process) bool) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	p, ok := proc.Get(pid)
	if !ok || !ours(p) {
		return 0
	}
	return pid
}

// guardState — последняя строка состояния стража: running PID T | restarting N | gave-up N | stopped | disabled.
func guardState() []string {
	b, _ := os.ReadFile(layout.GuardState())
	return strings.Fields(string(b))
}

// daemonUp — отвечает ли демон по сокету.
func daemonUp() bool {
	return control.Call(control.SocketPath(), control.Request{Cmd: "status"}, nil) == nil
}

// waitDaemon ждёт, пока демон ответит по сокету; ошибка — страж сдался или время вышло.
func waitDaemon(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemonUp() {
			return nil
		}
		if st := guardState(); len(st) > 0 && (st[0] == "gave-up" || st[0] == "stopped") {
			return errors.New("демон не запустился, страж остановился")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("демон не ответил за %v", timeout)
}

// preflight — настройки верны и режим поддерживается (иначе демон упадёт трижды, и страж сдастся).
func preflight() error {
	cfg, err := config.LoadFile(config.Path())
	if err != nil {
		return fmt.Errorf("настройки с ошибкой — исправьте (openmediad config): %w", err)
	}
	if cfg.General.Mode != "shim" {
		return fmt.Errorf("режим %q ещё не готов: укажите general.mode = \"shim\"", cfg.General.Mode)
	}
	return nil
}

// tail — последние n строк файла (для отчёта об ошибке).
func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
