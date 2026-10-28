package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"openmediad/internal/system/device"
	"openmediad/internal/system/iptables"
	"openmediad/internal/system/layout"
	"openmediad/internal/system/proc"
)

// speakerBox — песочница «колонка»: корень во временном каталоге (maind, aplay, init.d), корень смонтирован rw
// (mount не вызывается), iptables — подделка, aliced не отвечает.
func speakerBox(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("нужны Linux и root (команды установки проверяют права)")
	}
	root := t.TempDir()
	for _, d := range []string{"system/vendor/quasar", "usr/bin", "etc/init.d", "data", "tmp", "proc"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "system/vendor/quasar/maind"), nil, 0o755)
	os.WriteFile(filepath.Join(root, "usr/bin/aplay"), nil, 0o755)
	os.WriteFile(filepath.Join(root, "proc/mounts"), []byte("/dev/root / ext4 rw 0 0\n"), 0o644)
	// /proc для процессов — настоящий (pid стража и демона), mounts — из песочницы: подменяется только корень путей
	oldRoot, oldProc, oldIpt, oldAliced := layout.Root, proc.Root, iptables.Command, device.AlicedAddr
	layout.Root = root
	iptables.Command = func(args ...string) ([]byte, error) { return nil, nil }
	device.AlicedAddr = "127.0.0.1:1"
	t.Cleanup(func() {
		layout.Root, proc.Root, iptables.Command, device.AlicedAddr = oldRoot, oldProc, oldIpt, oldAliced
	})
	t.Setenv("OPENMEDIAD_CONFIG", filepath.Join(root, "data/opt/openmediad/openmediad.conf"))
	t.Setenv("OPENMEDIAD_RUN", filepath.Join(root, "tmp/openmediad"))
	return root
}

func TestInstallCycleCLI(t *testing.T) {
	root := speakerBox(t)
	code, out, errOut := runCLI("install", "-dry-run")
	if code != 0 || !strings.Contains(out, "• записать скрипт автозапуска") {
		t.Fatalf("install -dry-run: %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "data/opt/openmediad")); err == nil {
		t.Fatal("-dry-run что-то установил")
	}
	code, out, errOut = runCLI("install", "-yes", "-no-start")
	if code != 0 || !strings.Contains(out, "установлен в") {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	if !layout.Installed() {
		t.Fatal("после install не установлен")
	}
	if code, out, _ := runCLI("status"); code != ExitError || !strings.Contains(out, "автозапуск включён") {
		t.Errorf("status после установки: %d\n%s", code, out)
	}
	if code, out, _ := runCLI("disable"); code != 0 || !strings.Contains(out, "выключен") {
		t.Errorf("disable: %d %s", code, out)
	}
	if code, out, _ := runCLI("status"); !strings.Contains(out, "автозапуск выключен") {
		t.Errorf("status после disable: %d\n%s", code, out)
	}
	if code, _, _ := runCLI("enable"); code != 0 {
		t.Errorf("enable: %d", code)
	}
	code, out, errOut = runCLI("uninstall", "-yes")
	if code != 0 || !strings.Contains(out, "openmediad удалён") {
		t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc/init.d/S98openmediad")); err == nil {
		t.Error("скрипт автозапуска остался")
	}
	if code, out, _ := runCLI("uninstall", "-yes"); code != 0 || !strings.Contains(out, "не установлен") {
		t.Errorf("повторный uninstall: %d %s", code, out)
	}
}

func TestInstallRefusesForeignModel(t *testing.T) {
	speakerBox(t)
	iptables.Command = func(args ...string) ([]byte, error) { return []byte("nat: нет"), os.ErrInvalid }
	if code, _, errOut := runCLI("install", "-yes", "-no-start"); code != ExitError || !strings.Contains(errOut, "iptables") {
		t.Fatalf("без nat: %d %q", code, errOut)
	}
}
