package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"openmediad/internal/core/config"
	"openmediad/internal/core/platform/device"
	"openmediad/internal/core/platform/disk"
	"openmediad/internal/core/platform/initd"
	"openmediad/internal/core/platform/iptables"
	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/platform/proc"
	"openmediad/internal/core/platform/stock"
	"openmediad/internal/core/registry"
)

var doctorCmd = Command{
	Name:    "doctor",
	Summary: "проверить колонку и установку: что не так и как исправить",
	Help: `Проверки: модель и прошивка; root; aplay, maind, iptables (таблица nat); установка (бинарник, страж, скрипт
автозапуска совпадает с ожидаемым); правило iptables есть тогда и только тогда, когда демон работает; демон отвечает;
настройки верны; место на /data и свободная память; порты 19906 и 6701; редактор для openmediad config; счётчик падений.

Код выхода 1, если есть ошибки (✗). Предупреждения (!) работе не мешают.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "doctor")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := device.Check(); err != nil {
			return fmt.Errorf("%w (%v)", ErrNotDevice, err)
		}
		d := &doctor{c: c}
		d.run()
		c.Printf("\nошибок: %d, предупреждений: %d\n", d.fails, d.warns)
		if d.fails > 0 {
			return errSilent
		}
		return nil
	},
}

type doctor struct {
	c            *Ctx
	fails, warns int
}

func (d *doctor) ok(name, f string, a ...any) {
	d.c.Printf("✓ %-18s %s\n", name, fmt.Sprintf(f, a...))
}
func (d *doctor) info(name, f string, a ...any) {
	d.c.Printf("· %-18s %s\n", name, fmt.Sprintf(f, a...))
}
func (d *doctor) warn(name, f string, a ...any) {
	d.warns++
	d.c.Printf("! %-18s %s\n", name, fmt.Sprintf(f, a...))
}
func (d *doctor) fail(name, f string, a ...any) {
	d.fails++
	d.c.Printf("✗ %-18s %s\n", name, fmt.Sprintf(f, a...))
}

func (d *doctor) run() {
	// колонка
	if info, err := device.Identify(3 * time.Second); err != nil {
		d.warn("модель", "не проверена: %v", err)
	} else if !info.KnownModel() {
		d.fail("модель", "%s — openmediad сделан для %s", info, device.Model)
	} else if !info.KnownFirmware() {
		d.warn("модель", "%s — прошивка не проверялась, протокол мог измениться", info)
	} else {
		d.ok("модель", "%s", info)
	}
	if err := device.RequireRoot(); err != nil {
		d.fail("права", "%v — команды установки и запуска работают только от root", err)
	} else {
		d.ok("права", "root")
	}
	for _, p := range []string{"/usr/bin/aplay", device.Maind} {
		if _, err := os.Stat(layout.Root + p); err != nil {
			d.fail("файлы прошивки", "нет %s", p)
		} else {
			d.ok("файлы прошивки", "%s", p)
		}
	}
	nat := iptables.Usable()
	if nat != nil {
		d.fail("iptables", "%v", nat)
	} else {
		d.ok("iptables", "таблица nat доступна")
	}

	// установка
	installed := layout.Installed()
	switch {
	case installed:
		d.ok("установка", "%s", layout.Home())
		d.checkFile("страж", layout.InstalledGuard(), "")
		want := initd.Init(layout.HomeDir)
		if b, err := os.ReadFile(layout.Init()); err != nil {
			d.fail("автозапуск", "нет %s (обновление прошивки стёрло?) — openmediad install", layout.InitScript)
		} else if string(b) != want {
			d.warn("автозапуск", "%s отличается от ожидаемого — openmediad install перепишет", layout.InitScript)
		} else if exists(layout.AutostartOff()) {
			d.warn("автозапуск", "выключен (openmediad enable)")
		} else {
			d.ok("автозапуск", "%s", layout.InitScript)
		}
	case exists(layout.Init()):
		d.warn("установка", "скрипт автозапуска есть, а %s нет — openmediad uninstall уберёт остатки", layout.Bin())
	default:
		d.info("установка", "не установлен (openmediad install); start работает до перезагрузки")
	}

	// настройки
	if cfg, err := config.LoadFile(config.Path()); err != nil {
		d.fail("настройки", "%v — openmediad config", err)
	} else {
		for _, w := range cfg.Warnings {
			d.warn("настройки", "%s", w)
		}
		if exists(config.Path()) {
			d.ok("настройки", "%s", config.Path())
		} else {
			d.info("настройки", "файла нет — умолчания (openmediad config создаст)")
		}
	}
	if ed, err := findEditor(); err != nil {
		d.warn("редактор", "%v", err)
	} else {
		d.ok("редактор", "%s (для openmediad config)", ed[0])
	}

	// работа
	running := daemonUp()
	if running {
		d.ok("демон", "отвечает по %s", layoutSocket())
	} else if gp := guardRunning(); gp != 0 {
		d.warn("демон", "страж работает (pid %d), а демон не отвечает — перезапускается после падения?", gp)
	} else {
		d.info("демон", "не запущен")
	}
	if st := guardState(); len(st) > 1 && st[0] == "gave-up" {
		d.fail("страж", "сдался после %s падений подряд — %s, %s; openmediad start — попробовать снова", st[1], layout.DaemonErr(), layout.GuardLog())
	}
	if nat == nil {
		on, err := iptables.Present()
		switch {
		case err != nil:
			d.warn("правило", "%v", err)
		case on && !running:
			d.fail("правило", "стоит, а демон не отвечает — платформа не дойдёт до плеера; openmediad stop снимет")
		case !on && running:
			d.warn("правило", "демон работает, а правила нет — платформа идёт мимо openmediad (openmediad restart)")
		case on:
			d.ok("правило", "стоит, демон работает")
		default:
			d.ok("правило", "нет, демон не работает — колонка штатная")
		}
	}
	if c, err := stock.ReadConns(); err == nil {
		switch {
		case running && c.ViaShim == stock.Normal:
			d.ok("платформа", "%d соединения через шим", c.ViaShim)
		case running:
			d.warn("платформа", "через шим %d, напрямую %d (норма — 4 через шим)", c.ViaShim, c.Direct)
		case c.Direct == stock.Normal:
			d.ok("платформа", "%d соединения с mediad напрямую", c.Direct)
		default:
			d.warn("платформа", "напрямую %d, через шим %d — платформа ещё подключается?", c.Direct, c.ViaShim)
		}
	}
	if _, ok := stock.Mediad(); ok {
		d.ok("mediad", "штатный плеер работает")
	} else {
		d.fail("mediad", "не найден (maind --mediad)")
	}
	d.port("порт шима", "127.0.0.1:19906", running)
	d.port("отладочный порт", "0.0.0.0:6701", running)

	d.needs()

	// ресурсы
	if free, err := disk.FreeKB(layout.Root + "/data"); err == nil {
		if free < 20*1024 {
			d.warn("место на /data", "%s свободно", kbString(free))
		} else {
			d.ok("место на /data", "%s свободно", kbString(free))
		}
	}
	if avail, err := proc.MemAvailableKB(); err == nil {
		if avail < 40*1024 {
			d.warn("память", "MemAvailable %s — мало", kbString(avail))
		} else {
			d.ok("память", "MemAvailable %s", kbString(avail))
		}
	}
}

func (d *doctor) checkFile(name, path, want string) {
	if b, err := os.ReadFile(path); err != nil {
		d.fail(name, "нет %s — openmediad install", path)
	} else if want != "" && string(b) != want {
		d.warn(name, "%s отличается от ожидаемого", path)
	} else {
		d.ok(name, "%s", path)
	}
}

// port — порт свободен или занят нашим демоном.
func (d *doctor) port(name, addr string, running bool) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		d.ok(name, "%s свободен", addr)
		return
	}
	if running {
		d.ok(name, "%s занят (нашим демоном)", addr)
		return
	}
	if errors.Is(err, os.ErrPermission) {
		d.warn(name, "%s: %v", addr, err)
		return
	}
	d.fail(name, "%s занят чужой программой: %v", addr, strings.TrimSpace(err.Error()))
}

func layoutSocket() string { return layout.Run() + "/ctl.sock" }

// needs проверяет то, что компоненты этой сборки требуют от прошивки (registry.Need в паспорте компонента). Проверяются только
// требования, которые можно проверить; остальные просто перечислены в `openmediad components -v`.
func (d *doctor) needs() {
	for _, name := range registry.Compiled() {
		info, ok := registry.Lookup(name)
		if !ok {
			continue
		}
		for _, n := range info.Needs {
			present, known := checkNeed(n.ID)
			switch {
			case !known:
			case present:
				d.ok(name, "%s", n.Text)
			default:
				d.warn(name, "нет: %s", n.Text)
			}
		}
	}
}

// checkNeed проверяет одно требование. known=false — проверять нечем.
func checkNeed(id string) (present, known bool) {
	file := func(p string) (bool, bool) { return exists(layout.Root + p), true }
	port := func(addr string) (bool, bool) {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil, true
	}
	switch id {
	case registry.NeedGStreamer:
		return file(device.Maind)
	case registry.NeedAplay:
		return file("/usr/bin/aplay")
	case registry.NeedArecord:
		return file("/usr/bin/arecord")
	case registry.NeedCA:
		return file("/system/vendor/quasar/ca-certificates.crt")
	case registry.NeedSoundFiles:
		return file("/system/vendor/quasar/sounds")
	case registry.NeedBluez:
		return file("/run/dbus/system_bus_socket")
	case registry.NeedBluealsaRun:
		return file("/etc/init.d/S53bluealsa-aplay")
	case registry.NeedIOHub:
		return port("127.0.0.1:9903")
	case registry.NeedAliced:
		return port("127.0.0.1:9875")
	}
	return false, false
}
