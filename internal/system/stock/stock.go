// Package stock — штатный mediad колонки: найти, перезапустить, посчитать соединения платформы с ним.
//
// Перезапуск нужен, когда платформа уже подключена к mediad напрямую (openmediad start после загрузки): новое правило
// iptables действует только на новые соединения, а platformd переподключается, только когда mediad пропал. mediad
// поднимает штатный лаунчер Quasar за секунду; цена — теряется плеер, стоявший на паузе.
package stock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"openmediad/internal/system/proc"
)

// Mediad — процесс штатного плеера (maind --mediad).
func Mediad() (proc.Process, bool) {
	ps, err := proc.List()
	if err != nil {
		return proc.Process{}, false
	}
	m := proc.Find(ps, func(p proc.Process) bool { return p.Exe() == "maind" && p.HasArg("--mediad") })
	if len(m) == 0 {
		return proc.Process{}, false
	}
	return m[0], true
}

// Conns — соединения платформы с плеером: через шим и напрямую.
type Conns struct {
	ViaShim int // приняты шимом (порт 19906)
	Direct  int // клиенты плеера (порт 9906) мимо шима
}

// Норма — четыре соединения платформы с плеером.
const Normal = 4

// Порты в /proc/net/tcp — шестнадцатеричные.
const (
	shimPort    = "4DC2"     // 19906
	playerPort  = "26B2"     // 9906
	shimSrc     = "0200007F" // 127.0.0.2: соединения самого шима к mediad
	established = "01"
)

// ReadConns считает соединения по /proc/net/tcp. Сокет клиента, завёрнутый правилом к шиму, по-прежнему «думает»,
// что говорит с 9906: поэтому «через шим» считается со стороны шима, а «напрямую» — все клиенты 9906 минус они.
func ReadConns() (Conns, error) {
	f, err := os.Open(filepath.Join(proc.Root, "net", "tcp"))
	if err != nil {
		return Conns{}, err
	}
	defer f.Close()
	var c Conns
	clients := 0
	sc := bufio.NewScanner(f)
	sc.Scan() // заголовок
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 || fs[3] != established {
			continue
		}
		local, remote := fs[1], fs[2]
		switch {
		case strings.HasSuffix(local, ":"+shimPort):
			c.ViaShim++
		case strings.HasSuffix(remote, ":"+playerPort) && !strings.HasPrefix(local, shimSrc+":"):
			clients++
		}
	}
	c.Direct = max(clients-c.ViaShim, 0)
	return c, sc.Err()
}

// RestartMediad завершает mediad (SIGTERM; SIGKILL не используется) и ждёт, пока лаунчер поднимет новый.
func RestartMediad(ctx context.Context, logf func(string, ...any)) error {
	old, ok := Mediad()
	if !ok {
		return errors.New("штатный mediad не найден (maind --mediad)")
	}
	logf("перезапуск штатного mediad (pid %d), чтобы платформа переподключилась", old.PID)
	p, err := os.FindProcess(old.PID)
	if err == nil {
		err = p.Signal(syscall.SIGTERM)
	}
	if err != nil {
		return fmt.Errorf("SIGTERM mediad: %w", err)
	}
	if err := waitUntil(ctx, 30*time.Second, func() bool { return !proc.Alive(old.PID) }); err != nil {
		return fmt.Errorf("mediad не завершился за 30 с (SIGKILL не используется): %w", err)
	}
	if err := waitUntil(ctx, 60*time.Second, func() bool { _, ok := Mediad(); return ok }); err != nil {
		return fmt.Errorf("лаунчер не поднял mediad за 60 с — нужна перезагрузка колонки: %w", err)
	}
	m, _ := Mediad()
	logf("новый mediad: pid %d", m.PID)
	return nil
}

// WaitConns ждёт, пока cond не станет истинным для соединений платформы (или не выйдет время).
func WaitConns(ctx context.Context, timeout time.Duration, cond func(Conns) bool) (Conns, error) {
	var last Conns
	err := waitUntil(ctx, timeout, func() bool {
		c, err := ReadConns()
		if err != nil {
			return false
		}
		last = c
		return cond(c)
	})
	return last, err
}

// ErrTimeout — не дождались.
var ErrTimeout = errors.New("время вышло")

func waitUntil(ctx context.Context, timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Rerouter переводит платформу на шим: ждёт, пока все её соединения с плеером пойдут через шим, а если она держится
// за mediad напрямую — один раз перезапускает mediad (тогда platformd переподключается уже по правилу iptables).
type Rerouter struct {
	Logf     func(string, ...any)
	DaemonUp func() bool                     // жив ли демон (иначе ждать нечего)
	Restart  func(ctx context.Context) error // по умолчанию — RestartMediad
	Settle   time.Duration                   // сколько соединения должны быть прямыми, прежде чем перезапускать (3 с)
	Timeout  time.Duration                   // сколько ждать всего (по умолчанию 3 мин)
	Poll     time.Duration                   // как часто смотреть (500 мс)
}

// Run возвращает итоговые соединения; ошибка — платформа так и не пошла через шим.
func (r *Rerouter) Run(ctx context.Context) (Conns, error) {
	logf := r.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	restart := r.Restart
	if restart == nil {
		restart = func(ctx context.Context) error { return RestartMediad(ctx, logf) }
	}
	settle, timeout, poll := or(r.Settle, 3*time.Second), or(r.Timeout, 3*time.Minute), or(r.Poll, 500*time.Millisecond)
	deadline := time.Now().Add(timeout)
	var directSince time.Time
	restarted := false
	var c Conns
	for {
		var err error
		if c, err = ReadConns(); err != nil {
			return c, err
		}
		if c.ViaShim >= Normal {
			return c, nil
		}
		if r.DaemonUp != nil && !r.DaemonUp() {
			return c, errors.New("демон не работает")
		}
		switch {
		case c.Direct == 0:
			directSince = time.Time{}
		case directSince.IsZero():
			directSince = time.Now()
		case !restarted && time.Since(directSince) >= settle:
			logf("платформа подключена к mediad напрямую (%d соединения), через шим — %d", c.Direct, c.ViaShim)
			if err := restart(ctx); err != nil {
				return c, err
			}
			restarted, directSince = true, time.Time{}
		}
		if time.Now().After(deadline) {
			return c, fmt.Errorf("платформа не подключилась через шим за %v: через шим %d, напрямую %d", timeout, c.ViaShim, c.Direct)
		}
		select {
		case <-ctx.Done():
			return c, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
