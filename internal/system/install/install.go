// Package install — установка openmediad на колонку и удаление без следа (PLAN.md, §8.2–8.3).
//
// Всё своё лежит на /data/opt/openmediad. В rootfs (ubifs, только для чтения) установка кладёт два файла: скрипт
// автозапуска /etc/init.d/S98openmediad и ссылку /usr/bin/openmediad (чтобы команда работала из любого каталога).
// Каждая правка rootfs записана в backup/manifest.json (путь, был ли файл, sha256, копия оригинала) до того, как
// сделана; uninstall возвращает файлы по манифесту и сверяет результат.
//
// Ошибка на любом шаге установки — откат уже сделанного: колонка остаётся такой, какой была.
package install

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openmediad/internal/config"
	"openmediad/internal/control"
	"openmediad/internal/system/initd"
	"openmediad/internal/system/layout"
)

// LinkPath — ссылка на бинарник в PATH колонки.
const LinkPath = "/usr/bin/openmediad"

// initMarker — строка, по которой скрипт автозапуска узнаётся как наш (если манифест потерян).
const initMarker = "S98openmediad — автозапуск openmediad"

// Installer — установка и удаление.
type Installer struct {
	Self    string // этот бинарник (что копировать)
	Version string
	Logf    func(string, ...any)
	DryRun  bool // только показать план
	Root    Rootfs
	Now     func() time.Time
}

func (in *Installer) logf(f string, a ...any) {
	if in.Logf != nil {
		in.Logf(f, a...)
	}
}

// step — выполнить шаг (в пробном режиме только назвать его).
func (in *Installer) step(desc string, fn func() error) error {
	if in.DryRun {
		in.logf("  • %s", desc)
		return nil
	}
	in.logf("%s", desc)
	if err := fn(); err != nil {
		return fmt.Errorf("%s: %w", desc, err)
	}
	return nil
}

// Пути на колонке (без префикса layout.Root: так они записываются в файлы).
var (
	deviceBin   = layout.HomeDir + "/bin/openmediad"
	deviceState = layout.HomeDir + "/state"
)

// guardText — страж установленной копии.
func guardText() string {
	return initd.Guard(initd.GuardPaths{Bin: deviceBin, Run: runDirOnDevice(), State: deviceState})
}

// runDirOnDevice — рабочий каталог демона; в тестах — тот, что задан OPENMEDIAD_RUN.
func runDirOnDevice() string { return control.RunDir() }

// Install ставит openmediad (или обновляет установленный, сохраняя настройки). Возвращает откат: он возвращает колонку
// к состоянию до этого вызова (нужен, если после установки движок не запустился).
func (in *Installer) Install() (undo func() error, err error) {
	update := layout.Installed()
	var dataUndo, rootUndo []func() error
	undo = func() error {
		var errs []error
		if len(rootUndo) > 0 {
			errs = append(errs, in.Root.Edit(func() error {
				var es []error
				for i := len(rootUndo) - 1; i >= 0; i-- {
					es = append(es, rootUndo[i]())
				}
				return errors.Join(es...)
			}))
		}
		for i := len(dataUndo) - 1; i >= 0; i-- {
			errs = append(errs, dataUndo[i]())
		}
		return errors.Join(errs...)
	}
	defer func() {
		if err != nil && !in.DryRun {
			if e := undo(); e != nil {
				err = fmt.Errorf("%w; откат тоже с ошибкой: %v", err, e)
			} else {
				err = fmt.Errorf("%w (сделанное откачено)", err)
			}
		}
	}()
	if in.DryRun {
		in.logf("план:")
	}

	// 1. /data/opt/openmediad: каталоги, бинарник, страж, настройки по умолчанию
	if !update {
		created := !exists(layout.Home())
		if err := in.step("создать "+layout.Home(), func() error {
			for _, d := range []string{"bin", "state", "backup"} {
				if err := os.MkdirAll(filepath.Join(layout.Home(), d), 0o755); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return undo, err
		}
		if created {
			dataUndo = append(dataUndo, func() error { return removeExcept(layout.Home(), config.Path(), config.Path()+".bak") })
		}
	}
	if err := in.installBinary(&dataUndo); err != nil {
		return undo, err
	}
	oldGuard, _ := os.ReadFile(layout.InstalledGuard())
	if err := in.step("записать страж "+layout.InstalledGuard(), func() error {
		return writeAtomic(layout.InstalledGuard(), []byte(guardText()), 0o644)
	}); err != nil {
		return undo, err
	}
	dataUndo = append(dataUndo, func() error {
		if oldGuard == nil {
			return removeIfExists(layout.InstalledGuard())
		}
		return writeAtomic(layout.InstalledGuard(), oldGuard, 0o644)
	})
	if !exists(config.Path()) {
		if err := in.step("записать настройки по умолчанию "+config.Path(), func() error {
			return writeAtomic(config.Path(), []byte(config.Template()), 0o644)
		}); err != nil {
			return undo, err
		}
		dataUndo = append(dataUndo, func() error { return removeIfExists(config.Path()) })
	} else {
		in.logf("настройки %s сохраняются как есть", config.Path())
	}

	// 2. манифест — до правки rootfs: если что-то оборвётся посреди, uninstall будет знать, что трогали
	m, err := LoadManifest()
	if err != nil {
		return undo, fmt.Errorf("манифест %s: %w", ManifestPath(), err)
	}
	var oldManifest []byte
	if m == nil {
		m = &Manifest{Installed: in.now().Format(time.RFC3339)}
	} else {
		oldManifest, _ = os.ReadFile(ManifestPath())
		m.Updated = in.now().Format(time.RFC3339)
	}
	m.Version = in.Version
	initText := []byte(initd.Init(layout.HomeDir))
	initNew, err := in.planInit(m, initText)
	if err != nil {
		return undo, err
	}
	linkNew, linkSkip := in.planLink(m)
	if err := in.step("записать манифест бэкапа "+ManifestPath(), m.Save); err != nil {
		return undo, err
	}
	dataUndo = append(dataUndo, func() error {
		if oldManifest == nil {
			return removeIfExists(ManifestPath())
		}
		return writeAtomic(ManifestPath(), oldManifest, 0o644)
	})

	// 3. rootfs: скрипт автозапуска и ссылка
	oldInit, _ := os.ReadFile(layout.Init())
	edit := func() error {
		if err := in.step("записать скрипт автозапуска "+layout.InitScript, func() error {
			if err := writeAtomic(layout.Init(), initText, 0o755); err != nil {
				return err
			}
			got, err := os.ReadFile(layout.Init())
			if err != nil || !bytes.Equal(got, initText) {
				return fmt.Errorf("записанное не совпадает с ожидаемым (%v)", err)
			}
			return nil
		}); err != nil {
			return err
		}
		rootUndo = append(rootUndo, func() error {
			if initNew {
				return removeIfExists(layout.Init())
			}
			return writeAtomic(layout.Init(), oldInit, 0o755)
		})
		if linkNew {
			if err := in.step("ссылка "+LinkPath+" → "+deviceBin, func() error {
				return os.Symlink(deviceBin, layout.Root+LinkPath)
			}); err != nil {
				return err
			}
			rootUndo = append(rootUndo, func() error { return removeIfExists(layout.Root + LinkPath) })
		} else if linkSkip != "" {
			in.logf("%s", linkSkip)
		}
		return nil
	}
	if in.DryRun {
		in.logf("  • mount -o remount,rw / (только на время правки), sync, mount -o remount,ro /")
		return undo, edit()
	}
	if err := in.Root.Edit(edit); err != nil {
		return undo, err
	}
	return undo, nil
}

// installBinary копирует себя в bin/openmediad (через .new и переименование); старый бинарник сохраняется для отката.
func (in *Installer) installBinary(undo *[]func() error) error {
	dst := layout.Bin()
	if same(in.Self, dst) {
		in.logf("бинарник уже на месте: %s", dst)
		return nil
	}
	hadOld := exists(dst)
	if err := in.step(fmt.Sprintf("скопировать %s → %s", in.Self, dst), func() error {
		if hadOld {
			if err := copyFile(dst, dst+".old", 0o755); err != nil {
				return fmt.Errorf("сохранить прежний бинарник: %w", err)
			}
		}
		return copyFile(in.Self, dst, 0o755)
	}); err != nil {
		return err
	}
	*undo = append(*undo, func() error {
		if hadOld {
			return os.Rename(dst+".old", dst)
		}
		return removeIfExists(dst)
	})
	return nil
}

// planInit решает, что делать со скриптом автозапуска, и дописывает его в манифест (с копией чужого файла, если он был).
// Возвращает, создаётся ли файл заново (для отката).
func (in *Installer) planInit(m *Manifest, text []byte) (created bool, err error) {
	path := layout.Init()
	cur, readErr := os.ReadFile(path)
	exists := readErr == nil
	if e := m.entry(layout.InitScript); e != nil {
		e.SHA = sha(text)
		return !exists, nil
	}
	e := Entry{Path: layout.InitScript, SHA: sha(text)}
	switch {
	case !exists:
	case bytes.Contains(cur, []byte(initMarker)):
		// наш, оставшийся от прежней установки без манифеста
	default:
		e.Existed, e.Backup, e.OrigSHA = true, "S98openmediad.orig", sha(cur)
		if err := in.step("сохранить прежний "+layout.InitScript+" в бэкап", func() error {
			return writeAtomic(filepath.Join(layout.Backup(), e.Backup), cur, 0o644)
		}); err != nil {
			return false, err
		}
	}
	m.Files = append(m.Files, e)
	return !exists, nil
}

// planLink решает, создавать ли ссылку /usr/bin/openmediad: чужой файл с этим именем не трогается.
func (in *Installer) planLink(m *Manifest) (create bool, skip string) {
	path := layout.Root + LinkPath
	if target, err := readLink(path); err == nil {
		if target == deviceBin {
			if m.entry(LinkPath) == nil {
				m.Files = append(m.Files, Entry{Path: LinkPath, Link: deviceBin})
			}
			return false, ""
		}
		return false, fmt.Sprintf("%s уже есть и указывает на %s — не трогаю (запускайте %s)", LinkPath, target, deviceBin)
	}
	if exists(path) {
		return false, fmt.Sprintf("%s уже есть (чужой файл) — не трогаю (запускайте %s)", LinkPath, deviceBin)
	}
	if m.entry(LinkPath) == nil {
		m.Files = append(m.Files, Entry{Path: LinkPath, Link: deviceBin})
	}
	return true, ""
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func same(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeExcept удаляет каталог dir целиком, кроме файлов keep (они остаются на своих местах).
func removeExcept(dir string, keep ...string) error {
	kept := map[string]bool{}
	for _, k := range keep {
		if exists(k) {
			kept[filepath.Clean(k)] = true
		}
	}
	if len(kept) == 0 {
		return os.RemoveAll(dir)
	}
	var errs []error
	var walk func(d string) bool // true — каталог опустел
	walk = func(d string) bool {
		ents, err := os.ReadDir(d)
		if err != nil {
			errs = append(errs, err)
			return false
		}
		empty := true
		for _, e := range ents {
			p := filepath.Join(d, e.Name())
			switch {
			case kept[p]:
				empty = false
			case e.IsDir() && hasPrefixPath(kept, p):
				if !walk(p) {
					empty = false
					continue
				}
				errs = append(errs, os.Remove(p))
			default:
				errs = append(errs, os.RemoveAll(p))
			}
		}
		return empty
	}
	if walk(dir) {
		errs = append(errs, os.Remove(dir))
	}
	return errors.Join(errs...)
}

func hasPrefixPath(kept map[string]bool, dir string) bool {
	for k := range kept {
		if strings.HasPrefix(k, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, dst)
}

// readLink — цель символической ссылки с прямыми слэшами. На колонке она такая и есть; Windows (тесты на компьютере)
// отдаёт цель с обратными слэшами, и сравнение со своей ссылкой не сходилось бы.
func readLink(path string) (string, error) {
	t, err := os.Readlink(path)
	return filepath.ToSlash(t), err
}
