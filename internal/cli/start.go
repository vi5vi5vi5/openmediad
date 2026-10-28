package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"openmediad/internal/system/layout"
	"openmediad/internal/system/stock"
)

var startCmd = Command{
	Name:    "start",
	Summary: "включить движок до перезагрузки (через стража)",
	Usage:   "[-timeout 3m]",
	Help: `Запускает стража, а он — демон и правило iptables, которое ведёт платформу к плееру через openmediad. Если
платформа уже подключена к штатному mediad напрямую, mediad перезапускается (штатный лаунчер поднимает его за секунду;
плеер, стоявший на паузе, при этом теряется), и команда ждёт, пока все 4 соединения платформы пойдут через шим.

Не вышло — всё откатывается: правило снимается, демон останавливается, колонка работает штатно.

Без установки (openmediad install) движок работает до перезагрузки. Счётчик падений стража обнуляется.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "start")
		timeout := fs.Duration("timeout", 3*time.Minute, "сколько ждать, пока платформа подключится через шим")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		return start(c, *timeout)
	},
}

func start(c *Ctx, timeout time.Duration) error {
	if pid := guardRunning(); pid != 0 {
		return fmt.Errorf("уже запущен (страж pid %d); перезапустить — openmediad restart", pid)
	}
	if err := preflight(); err != nil {
		return err
	}
	guard, err := writeGuard()
	if err != nil {
		return fmt.Errorf("страж: %w", err)
	}
	c.printf("запуск openmediad (страж %s)\n", guard)
	os.Remove(layout.GuardState()) // старое «stopped» не должно сойти за ответ нового стража
	if err := launchGuard(guard, "start"); err != nil {
		return fmt.Errorf("страж не запустился: %w", err)
	}
	fail := func(err error) error {
		c.printf("не получилось: %v\nоткат: правило снимается, демон останавливается\n", err)
		runGuard(c, guard, "stop")
		if t := tail(layout.DaemonErr(), 5); t != "" {
			c.printf("последние ошибки демона (%s):\n%s\n", layout.DaemonErr(), t)
		}
		if t := tail(layout.GuardLog(), 5); t != "" {
			c.printf("журнал стража (%s):\n%s\n", layout.GuardLog(), t)
		}
		return errors.New("openmediad не запущен, колонка работает штатно")
	}
	if err := waitDaemon(15 * time.Second); err != nil {
		return fail(err)
	}
	c.printf("демон работает (pid %d)\n", daemonPID())
	r := &stock.Rerouter{
		Logf:     func(f string, a ...any) { c.printf(f+"\n", a...) },
		DaemonUp: daemonUp,
		Timeout:  timeout,
	}
	conns, err := r.Run(context.Background())
	if err != nil {
		return fail(err)
	}
	c.printf("готово: %d соединения платформы через шим. Состояние — openmediad status, журнал — openmediad logs\n", conns.ViaShim)
	if !layout.Installed() {
		c.printf("openmediad не установлен и выключится при перезагрузке; насовсем — openmediad install\n")
	}
	return nil
}

var stopCmd = Command{
	Name:    "stop",
	Summary: "выключить движок до перезагрузки (колонка работает штатно)",
	Usage:   "[-no-wait]",
	Help: `Страж снимает правило iptables и останавливает демон: играющая музыка встаёт на паузу с текущей позицией и
продолжается у штатного mediad, звонок таймера продолжает mediad. Потом команда ждёт, пока платформа подключится
к mediad напрямую (4 соединения).

Автозапуск при этом не выключается: после перезагрузки установленный openmediad запустится снова
(выключить — openmediad disable).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "stop")
		noWait := fs.Bool("no-wait", false, "не ждать, пока платформа подключится к mediad напрямую")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		return stop(c, !*noWait)
	},
}

func stop(c *Ctx, wait bool) error {
	running := guardRunning() != 0 || daemonPID() != 0
	guard, err := writeGuard()
	if err != nil {
		return fmt.Errorf("страж: %w", err)
	}
	if err := runGuard(c, guard, "stop"); err != nil {
		return fmt.Errorf("страж: %w", err)
	}
	if !running {
		c.printf("openmediad не был запущен; правило iptables снято (если было)\n")
		return nil
	}
	c.printf("openmediad остановлен, правило iptables снято\n")
	if !wait {
		return nil
	}
	conns, err := stock.WaitConns(context.Background(), 90*time.Second, func(c stock.Conns) bool {
		return c.Direct >= stock.Normal && c.ViaShim == 0
	})
	if err != nil {
		return fmt.Errorf("платформа не вернулась к mediad напрямую за 90 с: напрямую %d, через шим %d", conns.Direct, conns.ViaShim)
	}
	c.printf("платформа подключена к штатному mediad напрямую (%d соединения)\n", conns.Direct)
	return nil
}

var restartCmd = Command{
	Name:    "restart",
	Summary: "перезапустить движок (применить настройки, которые не применяются на ходу)",
	Usage:   "[-timeout 3m]",
	Help:    `То же, что openmediad stop и openmediad start подряд.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "restart")
		timeout := fs.Duration("timeout", 3*time.Minute, "сколько ждать, пока платформа подключится через шим")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		if err := preflight(); err != nil {
			return err
		}
		if err := stop(c, true); err != nil {
			return err
		}
		return start(c, *timeout)
	},
}

// rerouteCmd — для стража: после падения демона или при загрузке платформа может оказаться подключённой к mediad
// напрямую; команда дожидается её через шим и при нужде перезапускает mediad.
var rerouteCmd = Command{
	Name:    "reroute",
	Summary: "(для стража) провести платформу через шим",
	Hidden:  true,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "reroute")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		stamp := func() string { return time.Now().Format("2006-01-02 15:04:05") + " reroute: " }
		if err := waitDaemon(30 * time.Second); err != nil {
			c.printf("%s%v\n", stamp(), err)
			return errSilent
		}
		r := &stock.Rerouter{Logf: func(f string, a ...any) { c.printf(stamp()+f+"\n", a...) }, DaemonUp: daemonUp, Timeout: 5 * time.Minute}
		conns, err := r.Run(context.Background())
		if err != nil {
			c.printf("%s%v\n", stamp(), err)
			return errSilent
		}
		c.printf("%sплатформа подключена через шим (%d соединения)\n", stamp(), conns.ViaShim)
		return nil
	},
}
