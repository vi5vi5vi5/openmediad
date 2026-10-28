package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"openmediad/internal/buildinfo"
	"openmediad/internal/system/device"
	"openmediad/internal/system/disk"
	"openmediad/internal/system/install"
	"openmediad/internal/system/iptables"
	"openmediad/internal/system/layout"
)

var installCmd = Command{
	Name:    "install",
	Aliases: []string{"setup"},
	Summary: "установить на колонку: автозапуск вместо штатного пути, запуск",
	Usage:   "[-mode shim] [-dry-run] [-yes] [-force] [-no-start]",
	Help: `Устанавливает openmediad насовсем:

  1. проверки: модель и прошивка, root, iptables (таблица nat), aplay, место на /data;
  2. /data/opt/openmediad: бинарник (копия этого), страж guard.sh, настройки по умолчанию (если их ещё нет);
  3. бэкап того, что меняется в rootfs (backup/manifest.json с sha256);
  4. rootfs — на время одной правки в rw: скрипт автозапуска /etc/init.d/S98openmediad и ссылка /usr/bin/openmediad;
  5. запуск (как openmediad start) и проверка, что платформа подключилась через шим.

Ошибка на любом шаге — всё сделанное откатывается, колонка остаётся штатной. Повторный install — обновление:
настройки сохраняются, при неудаче возвращается прежняя версия.

  -dry-run   только показать план
  -yes       не спрашивать подтверждение
  -force     ставить на незнакомую модель колонки
  -no-start  установить, но не запускать сейчас (запустится после перезагрузки)`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "install")
		mode := fs.String("mode", "shim", "режим: shim (replace появится в фазе 12)")
		dry := fs.Bool("dry-run", false, "только показать план")
		yes := fs.Bool("yes", false, "не спрашивать подтверждение")
		force := fs.Bool("force", false, "ставить на незнакомую модель")
		noStart := fs.Bool("no-start", false, "не запускать после установки")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if *mode != "shim" {
			return UsageError{Msg: fmt.Sprintf("режим %q не поддерживается: пока только shim", *mode)}
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		self, _ = filepath.EvalSymlinks(self)
		if err := installChecks(c, self, *force); err != nil {
			return err
		}
		in := newInstaller(c, self)
		update := layout.Installed()
		if update {
			c.printf("openmediad уже установлен — это обновление (настройки сохраняются)\n")
		}
		in.DryRun = true
		if _, err := in.Install(); err != nil {
			return err
		}
		if !*noStart {
			c.printf("  • запустить (openmediad start) и дождаться платформы через шим\n")
		}
		if *dry {
			return nil
		}
		if !*yes && !ask(c, "Установить?") {
			return errors.New("отменено")
		}
		in.DryRun = false

		wasRunning := guardRunning() != 0 || daemonPID() != 0
		if wasRunning {
			c.printf("остановка работающего движка перед заменой\n")
			if err := stop(c, false); err != nil {
				return err
			}
		}
		undo, err := in.Install()
		if err != nil {
			if wasRunning {
				c.printf("запуск прежнего движка\n")
				start(c, 3*time.Minute)
			}
			return err
		}
		if !*noStart {
			if err := start(c, 3*time.Minute); err != nil {
				c.printf("движок не запустился — установка откатывается\n")
				if e := undo(); e != nil {
					c.printf("откат с ошибкой: %v\n", e)
				}
				if update && wasRunning {
					c.printf("запуск прежней версии\n")
					start(c, 3*time.Minute)
				}
				return err
			}
		}
		in.Cleanup()
		c.printf("\nopenmediad %s установлен в %s\n", buildinfo.Get().Version, layout.Home())
		c.printf("  автозапуск: %s — после перезагрузки движок запустится сам\n", layout.InitScript)
		c.printf("  состояние: openmediad status · журнал: openmediad logs · настройки: openmediad config\n")
		c.printf("  выключить до перезагрузки: openmediad stop · насовсем: openmediad disable · удалить: openmediad uninstall\n")
		return nil
	},
}

func newInstaller(c *Ctx, self string) *install.Installer {
	return &install.Installer{Self: self, Version: buildinfo.Get().Version, Logf: func(f string, a ...any) { c.printf(f+"\n", a...) }}
}

// installChecks — проверки перед установкой (часть doctor): незнакомая модель — отказ без -force.
func installChecks(c *Ctx, self string, force bool) error {
	if err := iptables.Usable(); err != nil {
		return fmt.Errorf("iptables: %w — режим shim без него невозможен", err)
	}
	if _, err := os.Stat(layout.Root + "/usr/bin/aplay"); err != nil {
		return errors.New("нет /usr/bin/aplay — openmediad выводит звук только через него")
	}
	if st, err := os.Stat(self); err == nil {
		need := st.Size()*3/1024 + 1024
		if free, err := disk.FreeKB(layout.Root + "/data"); err == nil && free < need {
			return fmt.Errorf("на /data свободно %d КБ, нужно не меньше %d", free, need)
		}
	}
	info, err := device.Identify(3 * time.Second)
	switch {
	case err != nil:
		c.printf("внимание: модель не проверена (%v)\n", err)
	case !info.KnownModel() && !force:
		return fmt.Errorf("это %s, а openmediad сделан для %s (Яндекс Станция Мини 3); поставить всё равно — -force", info, device.Model)
	case !info.KnownModel():
		c.printf("внимание: незнакомая модель %s — ставлю по -force\n", info)
	case !info.KnownFirmware():
		c.printf("колонка: %s\nвнимание: эта прошивка не проверялась (проверены: %v) — протокол мог измениться; следите за openmediad logs\n", info, device.KnownFirmware)
	default:
		c.printf("колонка: %s\n", info)
	}
	return nil
}

var uninstallCmd = Command{
	Name:    "uninstall",
	Summary: "удалить: вернуть колонку как было",
	Usage:   "[-dry-run] [-purge] [-yes]",
	Help: `Останавливает движок (музыка продолжается у штатного mediad), возвращает файлы rootfs по манифесту бэкапа
со сверкой sha256, удаляет /data/opt/openmediad и ждёт, пока платформа подключится к mediad напрямую.

Настройки остаются (для следующей установки); -purge удаляет и их. Файлы, изменённые после установки кем-то ещё,
не трогаются — о них будет предупреждение.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "uninstall")
		dry := fs.Bool("dry-run", false, "только показать план")
		purge := fs.Bool("purge", false, "удалить и настройки")
		yes := fs.Bool("yes", false, "не спрашивать подтверждение")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		m, _ := install.LoadManifest()
		if !layout.Installed() && m == nil && !exists(layout.Init()) && (!*purge || !exists(layout.Home())) {
			c.printf("openmediad не установлен\n")
			if exists(layout.Home()) {
				c.printf("остались настройки в %s (удалить — openmediad uninstall -purge)\n", layout.Home())
			}
			if guardRunning() != 0 || daemonPID() != 0 {
				c.printf("движок запущен до перезагрузки — выключить: openmediad stop\n")
			}
			return nil
		}
		in := newInstaller(c, "")
		in.DryRun = true
		c.printf("  • остановить движок и дождаться платформы у mediad напрямую\n")
		if _, err := in.Uninstall(*purge); err != nil {
			return err
		}
		if *dry {
			return nil
		}
		if !*yes && !ask(c, "Удалить openmediad?") {
			return errors.New("отменено")
		}
		in.DryRun = false
		if guardRunning() != 0 || daemonPID() != 0 {
			if err := stop(c, true); err != nil {
				return err
			}
		}
		warns, err := in.Uninstall(*purge)
		if err != nil {
			return err
		}
		c.printf("\nopenmediad удалён: файлы rootfs как до установки, колонка работает штатно\n")
		if len(warns) > 0 {
			c.printf("оставлено как есть (изменено после установки): %d — см. выше\n", len(warns))
		}
		if !*purge && exists(configPathHint()) {
			c.printf("настройки сохранены: %s (удалить — openmediad uninstall -purge или rm)\n", configPathHint())
		}
		return nil
	},
}

var enableCmd = Command{
	Name:    "enable",
	Summary: "включить автозапуск при загрузке",
	Help:    `Снимает отметку «автозапуск выключен». Сейчас движок не запускает: openmediad start.`,
	Run: func(c *Ctx, args []string) error {
		return setAutostart(c, args, "enable", true)
	},
}

var disableCmd = Command{
	Name:    "disable",
	Summary: "выключить автозапуск, не удаляя",
	Help: `После перезагрузки колонка работает штатно, openmediad остаётся установленным (включить — openmediad enable).
Работающий сейчас движок не останавливается: openmediad stop.`,
	Run: func(c *Ctx, args []string) error {
		return setAutostart(c, args, "disable", false)
	},
}

func setAutostart(c *Ctx, args []string, name string, on bool) error {
	fs := flags(c, name)
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := requireSpeaker(); err != nil {
		return err
	}
	if !layout.Installed() {
		return errors.New("openmediad не установлен (openmediad install)")
	}
	flag := layout.AutostartOff()
	if on {
		if err := os.Remove(flag); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		c.printf("автозапуск включён: после перезагрузки openmediad запустится сам\n")
		if guardRunning() == 0 {
			c.printf("сейчас движок не запущен — openmediad start\n")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(flag), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(flag, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	c.printf("автозапуск выключен: после перезагрузки колонка работает штатно\n")
	if guardRunning() != 0 {
		c.printf("движок пока работает — выключить сейчас: openmediad stop\n")
	}
	return nil
}
