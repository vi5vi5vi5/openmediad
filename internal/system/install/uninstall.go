package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"openmediad/internal/config"
	"openmediad/internal/system/layout"
)

// Uninstall возвращает rootfs по манифесту и удаляет /data/opt/openmediad (настройки остаются, если не purge).
// Движок к этому моменту должен быть остановлен (openmediad stop). Возвращает предупреждения: файлы, которые изменили
// после установки и которые поэтому оставлены как есть.
func (in *Installer) Uninstall(purge bool) (warnings []string, err error) {
	m, err := LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", ManifestPath(), err)
	}
	if m == nil {
		m = guessManifest()
		if len(m.Files) > 0 {
			in.logf("манифеста нет — ищу свои файлы по признакам")
		}
	}
	if in.DryRun {
		in.logf("план:")
	}
	warn := func(f string, a ...any) {
		w := fmt.Sprintf(f, a...)
		warnings = append(warnings, w)
		in.logf("внимание: %s", w)
	}

	edit := func() error {
		var errs []error
		for _, e := range m.Files {
			e := e
			path := layout.Root + e.Path
			switch {
			case e.Link != "":
				target, err := os.Readlink(path)
				switch {
				case errors.Is(err, os.ErrNotExist):
				case err != nil || target != e.Link:
					warn("%s изменён после установки — оставлен как есть", e.Path)
				default:
					errs = append(errs, in.step("удалить ссылку "+e.Path, func() error { return os.Remove(path) }))
				}
			case e.Existed:
				orig, err := os.ReadFile(filepath.Join(layout.Backup(), e.Backup))
				if err != nil || sha(orig) != e.OrigSHA {
					errs = append(errs, fmt.Errorf("копия оригинала %s повреждена или пропала — файл не восстановлен", e.Path))
					continue
				}
				errs = append(errs, in.step("вернуть исходный "+e.Path+" из бэкапа", func() error {
					if err := writeAtomic(path, orig, 0o755); err != nil {
						return err
					}
					if got, err := fileSHA(path); err != nil || got != e.OrigSHA {
						return fmt.Errorf("после восстановления sha256 не совпадает с оригиналом")
					}
					return nil
				}))
			default:
				cur, err := os.ReadFile(path)
				switch {
				case errors.Is(err, os.ErrNotExist):
				case err != nil:
					errs = append(errs, err)
				case sha(cur) != e.SHA && !bytes.Contains(cur, []byte(initMarker)):
					warn("%s изменён после установки — оставлен как есть", e.Path)
				default:
					errs = append(errs, in.step("удалить "+e.Path, func() error { return os.Remove(path) }))
				}
			}
		}
		return errors.Join(errs...)
	}
	if len(m.Files) > 0 {
		if in.DryRun {
			in.logf("  • mount -o remount,rw / (только на время правки), sync, mount -o remount,ro /")
			edit()
		} else if err := in.Root.Edit(edit); err != nil {
			return warnings, err
		}
	}

	if purge {
		err = in.step("удалить "+layout.Home()+" целиком (с настройками)", func() error { return os.RemoveAll(layout.Home()) })
	} else {
		err = in.step("удалить "+layout.Home()+" (настройки "+config.Path()+" остаются)", func() error {
			return removeExcept(layout.Home(), config.Path(), config.Path()+".bak")
		})
	}
	if err != nil || in.DryRun {
		return warnings, err
	}
	return warnings, verify(m)
}

// verify — после удаления файлы rootfs совпадают с тем, что было до установки.
func verify(m *Manifest) error {
	var errs []error
	for _, e := range m.Files {
		path := layout.Root + e.Path
		if e.Existed {
			if got, err := fileSHA(path); err != nil || got != e.OrigSHA {
				errs = append(errs, fmt.Errorf("%s не совпадает с оригиналом", e.Path))
			}
			continue
		}
		if e.Link != "" {
			if t, err := os.Readlink(path); err == nil && t == e.Link {
				errs = append(errs, fmt.Errorf("%s не удалён", e.Path))
			}
			continue
		}
		if got, err := fileSHA(path); err == nil && got == e.SHA {
			errs = append(errs, fmt.Errorf("%s не удалён", e.Path))
		}
	}
	return errors.Join(errs...)
}

// guessManifest — свои файлы по признакам, если манифест потерян: скрипт с нашей строкой, ссылка на наш бинарник.
func guessManifest() *Manifest {
	m := &Manifest{}
	if b, err := os.ReadFile(layout.Init()); err == nil && bytes.Contains(b, []byte(initMarker)) {
		m.Files = append(m.Files, Entry{Path: layout.InitScript, SHA: sha(b)})
	}
	if t, err := os.Readlink(layout.Root + LinkPath); err == nil && t == deviceBin {
		m.Files = append(m.Files, Entry{Path: LinkPath, Link: deviceBin})
	}
	return m
}

// Cleanup убирает прежний бинарник, сохранённый на случай отката (вызывается, когда новый уже работает).
func (in *Installer) Cleanup() { removeIfExists(layout.Bin() + ".old") }
