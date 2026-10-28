package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openmediad/internal/config"
	"openmediad/internal/system/layout"
	"openmediad/internal/system/proc"
)

// box — песочница: корень колонки во временном каталоге, корень «смонтирован только для чтения», mount — подделка.
type box struct {
	t       *testing.T
	root    string
	self    string
	mounts  []string
	failRW  bool
	in      *Installer
	logText strings.Builder
}

func newBox(t *testing.T) *box {
	b := &box{t: t, root: t.TempDir()}
	for _, d := range []string{"etc/init.d", "usr/bin", "proc", "tmp"} {
		os.MkdirAll(filepath.Join(b.root, d), 0o755)
	}
	os.WriteFile(filepath.Join(b.root, "proc", "mounts"), []byte("ubi0:rootfs / ubifs ro,relatime 0 0\n"), 0o644)
	oldRoot, oldProc := layout.Root, proc.Root
	layout.Root, proc.Root = b.root, filepath.Join(b.root, "proc")
	t.Cleanup(func() { layout.Root, proc.Root = oldRoot, oldProc })
	t.Setenv("OPENMEDIAD_CONFIG", filepath.Join(b.root, "data/opt/openmediad/openmediad.conf"))
	t.Setenv("OPENMEDIAD_RUN", filepath.Join(b.root, "tmp/openmediad"))
	b.self = filepath.Join(b.root, "tmp", "openmediad-new")
	os.WriteFile(b.self, []byte("binary v1"), 0o755)
	b.in = &Installer{
		Self: b.self, Version: "1",
		Logf: func(f string, a ...any) { b.logText.WriteString(strings.TrimSpace(sprintf(f, a...)) + "\n") },
		Root: Rootfs{
			Remount: func(rw bool) error {
				if rw && b.failRW {
					return errors.New("busy")
				}
				b.mounts = append(b.mounts, map[bool]string{true: "rw", false: "ro"}[rw])
				return nil
			},
			Sync: func() {},
		},
	}
	return b
}

func (b *box) path(p string) string { return filepath.Join(b.root, p) }

func (b *box) read(p string) string {
	c, _ := os.ReadFile(b.path(p))
	return string(c)
}

func (b *box) exists(p string) bool {
	_, err := os.Lstat(b.path(p))
	return err == nil
}

func TestFreshInstallAndUninstall(t *testing.T) {
	b := newBox(t)
	if _, err := b.in.Install(); err != nil {
		t.Fatalf("Install: %v\n%s", err, b.logText.String())
	}
	if b.read("data/opt/openmediad/bin/openmediad") != "binary v1" {
		t.Error("бинарник не скопирован")
	}
	if !strings.Contains(b.read("etc/init.d/S98openmediad"), "D=/data/opt/openmediad") {
		t.Error("скрипт автозапуска не записан")
	}
	if l, _ := os.Readlink(b.path("usr/bin/openmediad")); l != "/data/opt/openmediad/bin/openmediad" {
		t.Errorf("ссылка: %q", l)
	}
	if g := b.read("data/opt/openmediad/guard.sh"); !strings.Contains(g, `BIN="/data/opt/openmediad/bin/openmediad"`) {
		t.Error("страж не записан или пути не те")
	}
	if !strings.Contains(b.read("data/opt/openmediad/openmediad.conf"), "[general]") {
		t.Error("настройки по умолчанию не записаны")
	}
	if strings.Join(b.mounts, ",") != "rw,ro" {
		t.Errorf("remount: %v, ждали rw,ro", b.mounts)
	}
	m, _ := LoadManifest()
	if m == nil || len(m.Files) != 2 || m.Files[0].Existed || m.Files[0].SHA == "" {
		t.Fatalf("манифест: %+v", m)
	}
	if !layout.Installed() {
		t.Fatal("Installed() после установки — false")
	}

	b.mounts = nil
	warns, err := b.in.Uninstall(false)
	if err != nil || len(warns) > 0 {
		t.Fatalf("Uninstall: %v %v\n%s", err, warns, b.logText.String())
	}
	if b.exists("etc/init.d/S98openmediad") || b.exists("usr/bin/openmediad") {
		t.Error("rootfs не вернулся к исходному")
	}
	if !b.exists("data/opt/openmediad/openmediad.conf") || b.exists("data/opt/openmediad/bin") || b.exists("data/opt/openmediad/guard.sh") {
		t.Error("без -purge должны остаться только настройки")
	}
	if strings.Join(b.mounts, ",") != "rw,ro" {
		t.Errorf("remount при удалении: %v", b.mounts)
	}
	if _, err := b.in.Uninstall(true); err != nil || b.exists("data/opt/openmediad") {
		t.Errorf("purge: %v, каталог остался: %v", err, b.exists("data/opt/openmediad"))
	}
}

func TestForeignFilesAreBackedUpOrLeft(t *testing.T) {
	b := newBox(t)
	os.WriteFile(b.path("etc/init.d/S98openmediad"), []byte("#!/bin/sh\necho чужой\n"), 0o755)
	os.WriteFile(b.path("usr/bin/openmediad"), []byte("чужой бинарник"), 0o755)
	if _, err := b.in.Install(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.logText.String(), "чужой файл") {
		t.Error("нет предупреждения о чужом /usr/bin/openmediad")
	}
	if b.read("usr/bin/openmediad") != "чужой бинарник" {
		t.Error("чужой /usr/bin/openmediad тронут")
	}
	if _, err := b.in.Uninstall(false); err != nil {
		t.Fatal(err)
	}
	if b.read("etc/init.d/S98openmediad") != "#!/bin/sh\necho чужой\n" {
		t.Errorf("чужой скрипт не восстановлен: %q", b.read("etc/init.d/S98openmediad"))
	}
	if b.read("usr/bin/openmediad") != "чужой бинарник" {
		t.Error("чужой /usr/bin/openmediad удалён")
	}
}

func TestUpdateKeepsConfigAndUndoRestoresOldBinary(t *testing.T) {
	b := newBox(t)
	if _, err := b.in.Install(); err != nil {
		t.Fatal(err)
	}
	conf := b.path("data/opt/openmediad/openmediad.conf")
	text, _, _ := config.ReadText(conf)
	text, _ = config.Set(text, "general.log_level", "trace")
	config.WriteFile(conf, text)

	os.WriteFile(b.self, []byte("binary v2"), 0o755)
	b.in.Version = "2"
	undo, err := b.in.Install()
	if err != nil {
		t.Fatal(err)
	}
	if b.read("data/opt/openmediad/bin/openmediad") != "binary v2" {
		t.Error("бинарник не обновлён")
	}
	if !strings.Contains(b.read("data/opt/openmediad/openmediad.conf"), `log_level = "trace"`) {
		t.Error("обновление затёрло настройки")
	}
	m, _ := LoadManifest()
	if m.Version != "2" || m.Updated == "" || len(m.Files) != 2 {
		t.Errorf("манифест после обновления: %+v", m)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if b.read("data/opt/openmediad/bin/openmediad") != "binary v1" {
		t.Error("откат обновления не вернул прежний бинарник")
	}
	if !b.exists("etc/init.d/S98openmediad") {
		t.Error("откат обновления удалил скрипт автозапуска прежней установки")
	}
	if m, _ := LoadManifest(); m.Version != "1" {
		t.Errorf("откат не вернул манифест: %+v", m)
	}
}

func TestFailedInstallLeavesNothing(t *testing.T) {
	b := newBox(t)
	b.failRW = true
	if _, err := b.in.Install(); err == nil || !strings.Contains(err.Error(), "откачено") {
		t.Fatalf("ждали ошибку с откатом, получили %v", err)
	}
	if b.exists("data/opt/openmediad") || b.exists("etc/init.d/S98openmediad") || b.exists("usr/bin/openmediad") {
		t.Error("после неудачной установки остались файлы")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	b := newBox(t)
	b.in.DryRun = true
	if _, err := b.in.Install(); err != nil {
		t.Fatal(err)
	}
	if b.exists("data/opt/openmediad") || b.exists("etc/init.d/S98openmediad") || len(b.mounts) > 0 {
		t.Error("пробная установка что-то изменила")
	}
	if !strings.Contains(b.logText.String(), "• записать скрипт автозапуска") {
		t.Errorf("план не напечатан:\n%s", b.logText.String())
	}
}

func TestUninstallWithoutManifestFindsOwnFiles(t *testing.T) {
	b := newBox(t)
	if _, err := b.in.Install(); err != nil {
		t.Fatal(err)
	}
	os.Remove(ManifestPath())
	if _, err := b.in.Uninstall(false); err != nil {
		t.Fatal(err)
	}
	if b.exists("etc/init.d/S98openmediad") || b.exists("usr/bin/openmediad") {
		t.Error("без манифеста свои файлы не найдены")
	}
}

func TestEditedInitScriptIsLeft(t *testing.T) {
	b := newBox(t)
	if _, err := b.in.Install(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(b.path("etc/init.d/S98openmediad"), []byte("#!/bin/sh\n# правка руками\n"), 0o755)
	warns, err := b.in.Uninstall(false)
	if err != nil || len(warns) != 1 || !b.exists("etc/init.d/S98openmediad") {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
