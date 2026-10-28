// Package app — сборка демона openmediad: настройки → журнал → движок → автомат mediad → компоненты сборки → шим на шине.
// Это единственное место, где всё соединяется (PLAN.md, §5.3); остальные пакеты друг о друге знают только через интерфейсы.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"openmediad/internal/buildinfo"
	"openmediad/internal/config"
	"openmediad/internal/control"
	"openmediad/internal/engine"
	"openmediad/internal/journal"
	"openmediad/internal/quasar/explain"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/quasar/shim"
	"openmediad/internal/quasar/wire"
	"openmediad/internal/sink/speaker"
	"openmediad/internal/system/clock"
)

// Адреса шима: клиенты (platformd) попадают на Listen правилом iptables стража; к настоящему mediad шим ходит с адреса Src,
// чтобы то же правило не заворачивало его самого (docs/ARCHITECTURE.md).
const (
	DefaultListen   = "127.0.0.1:19906"
	DefaultUpstream = "127.0.0.1:9906"
	DefaultSrc      = "127.0.0.2"
)

// Options — как запустить демон.
type Options struct {
	ConfigPath string
	Foreground bool   // копировать журнал в stderr (ручной запуск `openmediad run`)
	Listen     string // где слушать платформу (по умолчанию DefaultListen)
	Upstream   string // настоящий mediad (по умолчанию DefaultUpstream)
	Src        string // локальный адрес соединений к mediad (по умолчанию DefaultSrc; пусто — системный)
	Socket     string // сокет команд (по умолчанию control.SocketPath())
}

// daemon — всё, что живёт, пока работает демон.
type daemon struct {
	opt     Options
	started time.Time

	mu  sync.Mutex
	cfg *config.Config

	j       *journal.Journal
	eng     *engine.Engine
	rt      *router
	m       *mediad.Machine
	proxy   *shim.Proxy
	comps   []*component
	closers []func()
	dup     dedup
}

// Run запускает демон и работает, пока не придёт SIGTERM или SIGINT (или не отменён ctx). Ошибка — демон не смог стартовать.
func Run(ctx context.Context, o Options) error {
	debug.SetGCPercent(25) // колонка слабая: частая сборка мусора дешевле лишних мегабайт
	if o.ConfigPath == "" {
		o.ConfigPath = config.Path()
	}
	if o.Listen == "" {
		o.Listen = DefaultListen
	}
	if o.Upstream == "" {
		o.Upstream = DefaultUpstream
	}
	if o.Socket == "" {
		o.Socket = control.SocketPath()
	}
	cfg, err := config.LoadFile(o.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.General.Mode == "replace" {
		return errors.New("режим replace ещё не готов (фаза 12 плана): укажите mode = \"shim\"")
	}
	d := &daemon{opt: o, cfg: cfg, started: time.Now()}
	defer func() {
		for i := len(d.closers) - 1; i >= 0; i-- {
			d.closers[i]()
		}
	}()
	d.setupJournal()
	d.j.Log(journal.Info, "system", "запуск openmediad: "+buildinfo.Get().Line(), "", map[string]any{"config": o.ConfigPath, "pid": os.Getpid()})
	for _, w := range cfg.Warnings {
		d.j.Log(journal.Warn, "system", "настройки: "+w, "", nil)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.setupEngine()
	if err := d.setupShim(); err != nil {
		return err
	}
	// сокет команд — до компонентов: второй демон на том же сокете не должен ни вставать в разрыв шины, ни занимать порты
	ctl := &control.Server{Path: o.Socket, Handle: d.handle, Logf: d.j.Logf("system")}
	ctlErr := make(chan error, 1)
	go func() { ctlErr <- ctl.Run(ctx) }()
	select {
	case err := <-ctlErr:
		if err != nil {
			return fmt.Errorf("сокет команд: %w", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	d.startComponents(ctx)
	d.watchAliced(ctx, d.componentOn("debug"))

	go d.signals(ctx, cancel)
	d.j.Log(journal.Info, "shim", fmt.Sprintf("шим: %s → %s (исходящие с %s)", o.Listen, o.Upstream, o.Src), "", nil)
	err = d.proxy.Run(ctx)
	d.j.Log(journal.Info, "system", "openmediad остановлен", "", nil)
	return err
}

// setupJournal — журнал по настройкам: кольцо в памяти, копия в stderr при ручном запуске, файл — если задан.
func (d *daemon) setupJournal() {
	g := d.cfg.General
	d.j = &journal.Journal{Cap: g.LogEntries}
	d.j.SetLevel(g.LogLevel)
	var outs []io.Writer
	if d.opt.Foreground {
		outs = append(outs, os.Stderr)
	}
	if g.LogFile != "" {
		fw := &journal.FileWriter{Path: g.LogFile, Max: int64(g.LogFileMaxKB) << 10 / 2}
		outs = append(outs, fw)
		d.closers = append(d.closers, func() { fw.Close() })
	}
	if len(outs) > 0 {
		d.j.Out = io.MultiWriter(outs...)
	}
}

// setupEngine — движок с выходами по маршрутам настроек.
func (d *daemon) setupEngine() {
	sp := &speaker.Sink{Aplay: d.cfg.Speaker.Aplay, Device: d.cfg.Speaker.Device}
	d.rt = newRouter(d.cfg.Route, sp)
	d.eng = &engine.Engine{Router: d.rt, Logf: d.j.Logf("engine")}
}

// setupShim — шим на шине и автомат mediad. Бэкенды автомата подключают компоненты (startComponents).
func (d *daemon) setupShim() error {
	var src net.IP
	if d.opt.Src != "" {
		if src = net.ParseIP(d.opt.Src); src == nil {
			return fmt.Errorf("адрес исходящих соединений: не адрес %q", d.opt.Src)
		}
	}
	d.proxy = &shim.Proxy{Listen: d.opt.Listen, Upstream: d.opt.Upstream, Src: src, Logf: d.j.Logf("shim")}
	d.m = &mediad.Machine{
		Emit: func(body []byte) {
			d.proxy.Broadcast(wire.Wrap(wire.TypeAudioEvent, body))
			d.j.Add(explain.Synthesized(body))
		},
		Forward: func(ctx any, body []byte) {
			if c, ok := ctx.(*shim.Conn); ok {
				if err := d.proxy.ForwardTo(c, wire.Wrap(wire.TypeAudioCommand, body)); err != nil {
					d.j.Log(journal.Warn, "shim", fmt.Sprintf("возврат команды mediad: %v", err), "", nil)
				}
			}
		},
		Now:  time.Now,
		Mono: func() uint64 { return uint64(clock.Mono() / 1e6) },
		After: func(dur time.Duration, f func()) func() {
			t := time.AfterFunc(dur, f)
			return func() { t.Stop() }
		},
		Back:    mediad.NoBackend{},
		NoMusic: true,
		Duck:    d.cfg.Focus.DuckGain,
		Logf:    d.j.Logf("player"),
		Note:    func(kind, id, detail string) { d.j.Add(explain.Note(kind, id, detail)) },
	}
	d.proxy.Intercept = d.intercept
	d.proxy.Tap = d.tap
	return nil
}

// signals: SIGTERM/SIGINT — мягкая передача плееров mediad и остановка; SIGHUP — перечитать настройки.
func (d *daemon) signals(ctx context.Context, cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-sig:
			if s == syscall.SIGHUP {
				msg, err := d.reload()
				if err != nil {
					d.j.Log(journal.Warn, "system", "настройки не перечитаны: "+err.Error(), "", nil)
				} else {
					d.j.Log(journal.Info, "system", msg, "", nil)
				}
				continue
			}
			d.j.Log(journal.Info, "system", "остановка по сигналу "+s.String(), "Плееры передаются штатному mediad: музыка встаёт на паузу с текущей позицией, звонок продолжается у mediad.", nil)
			d.m.Handoff() // платформа должна узнать о паузе, пока соединения шима ещё открыты
			time.Sleep(150 * time.Millisecond)
			cancel()
			return
		}
	}
}

func timeSince(t time.Time) time.Duration { return time.Since(t).Truncate(time.Second) }
