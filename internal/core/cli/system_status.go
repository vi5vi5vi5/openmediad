package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/platform/device"
	"openmediad/internal/core/platform/iptables"
	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/platform/proc"
	"openmediad/internal/core/platform/stock"
)

// systemStatus — строки о системной части для `status`: установка, страж, правило iptables, платформа, штатный mediad.
// На компьютере (не колонка) — пусто.
func systemStatus() []string {
	if device.Check() != nil {
		return nil
	}
	var out []string
	add := func(k, f string, a ...any) { out = append(out, fmt.Sprintf("%-11s %s", k+":", fmt.Sprintf(f, a...))) }

	switch {
	case !layout.Installed():
		self, _ := os.Executable()
		add("установка", "нет (работает %s до перезагрузки; насовсем — openmediad install)", self)
	case !exists(layout.Init()):
		add("установка", "%s, но скрипта автозапуска %s нет (обновление прошивки?) — openmediad install", layout.Home(), layout.Init())
	case exists(layout.AutostartOff()):
		add("установка", "%s, автозапуск выключен (openmediad enable)", layout.Home())
	default:
		add("установка", "%s, автозапуск включён", layout.Home())
	}

	max := config.Defaults().Fallback.MaxCrashes
	if cfg, err := config.LoadFile(config.Path()); err == nil {
		max = cfg.Fallback.MaxCrashes
	}
	crashes := 0
	if b, err := os.ReadFile(layout.Crashes()); err == nil {
		crashes, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	st := guardState()
	gp := guardRunning()
	switch {
	case gp != 0 && len(st) > 0 && st[0] == "restarting":
		add("страж", "работает (pid %d), перезапускает демон после падения: быстрых падений подряд %d из %d", gp, crashes, max)
	case gp != 0:
		add("страж", "работает (pid %d); быстрых падений подряд %d из %d", gp, crashes, max)
	case len(st) > 1 && st[0] == "gave-up":
		add("страж", "сдался после %s падений подряд — колонка работает штатно; ошибки демона — %s, журнал стража — %s; запустить снова — openmediad start", st[1], layout.DaemonErr(), layout.GuardLog())
	case len(st) > 0 && st[0] == "disabled":
		add("страж", "не запущен: автозапуск выключен")
	default:
		add("страж", "не запущен")
	}

	if on, err := iptables.Present(); err != nil {
		add("правило", "не проверено: %v", err)
	} else if on {
		add("правило", "стоит (iptables: 9906 → 19906)")
	} else {
		add("правило", "нет — платформа идёт к mediad напрямую")
	}
	if c, err := stock.ReadConns(); err == nil {
		add("платформа", "через шим %d, напрямую к mediad %d (норма — %d)", c.ViaShim, c.Direct, stock.Normal)
	}
	if m, ok := stock.Mediad(); ok {
		pss := ""
		if kb, err := proc.PssKB(m.PID); err == nil {
			pss = ", Pss " + kbString(kb)
		}
		add("mediad", "pid %d%s (штатный плеер работает рядом)", m.PID, pss)
	} else {
		add("mediad", "не найден (maind --mediad) — платформа без плеера; лаунчер обычно поднимает его за секунду")
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
