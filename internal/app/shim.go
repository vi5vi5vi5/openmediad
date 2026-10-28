package app

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"openmediad/internal/journal"
	"openmediad/internal/quasar/explain"
	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/observe"
	"openmediad/internal/quasar/shim"
	"openmediad/internal/quasar/wire"
)

// intercept решает судьбу каждого кадра платформы: команду плееру, которую openmediad взял себе, автомат обрабатывает
// сам (штатному mediad она не уходит); всё остальное — как есть. Вызывается из горутины соединения: только быстрое.
func (d *daemon) intercept(c *shim.Conn, frame []byte) bool {
	m, err := wire.Unwrap(frame)
	if err != nil || m.Type != wire.TypeAudioCommand {
		return false
	}
	cmd := msg.AudioCommand(m.Body)
	eaten := d.m.Handle(c, cmd)
	if eaten {
		d.j.Add(explain.Intercepted(c.ID, cmd))
	}
	return eaten
}

// tap пишет в журнал кадры, прошедшие через шим как есть. События плеера mediad рассылает всем четырём соединениям
// платформы: в журнал идёт одна копия.
func (d *daemon) tap(c *shim.Conn, dir shim.Dir, frame []byte, _ time.Duration) {
	if dir == shim.ToClient && d.dup.seen(frame) {
		return
	}
	e, ok := explain.DescribeFrame(dir == shim.ToService, frame)
	if !ok || journal.Rank(e.Lvl) < journal.Rank(d.j.Level()) {
		return
	}
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	e.Data["conn"] = c.ID
	d.j.Add(e)
}

// dedup узнаёт кадры, уже виденные за последние 300 мс (копии одной рассылки на другие соединения).
type dedup struct {
	mu   sync.Mutex
	last [32]struct {
		h uint64
		t time.Time
	}
	next int
}

func (dd *dedup) seen(frame []byte) bool {
	h := fnv.New64a()
	h.Write(frame)
	sum := h.Sum64()
	now := time.Now()
	dd.mu.Lock()
	defer dd.mu.Unlock()
	for _, e := range dd.last {
		if e.h == sum && now.Sub(e.t) < 300*time.Millisecond {
			return true
		}
	}
	dd.last[dd.next].h, dd.last[dd.next].t = sum, now
	dd.next = (dd.next + 1) % len(dd.last)
	return false
}

// watchAliced следит за диалогом Алисы (только чтение): директивы ответа (выключить или включить Bluetooth) уходят
// компонентам, а журнал (что услышала, думает ли, говорит ли) — только с отладочным портом: в него попадают распознанные
// фразы.
func (d *daemon) watchAliced(ctx context.Context, logLines bool) {
	var lastKey string
	var lastAt time.Time
	o := &observe.Observer{
		Targets: []observe.Target{{Name: "aliced", Addr: "127.0.0.1:9875"}},
		OnReply: func(req string, directives []string) {
			// ответ приходит в нескольких состояниях Алисы; номер запроса бывает пустым (stop_bluetooth)
			key := req + " " + strings.Join(directives, ",")
			if key == lastKey && time.Since(lastAt) < 5*time.Second {
				return
			}
			lastKey, lastAt = key, time.Now()
			for _, name := range directives {
				switch {
				case strings.HasPrefix(name, "stop_bluetooth"):
					d.platformBluetooth(false)
				case strings.HasPrefix(name, "start_bluetooth"):
					d.platformBluetooth(true)
				}
			}
		},
		OnLine: func(who, text string) {
			if !logLines {
				return
			}
			lvl, why := journal.Info, ""
			switch {
			case strings.HasPrefix(text, "AliceState"):
				switch {
				case strings.Contains(text, "LISTENING") && strings.Contains(text, "text="):
					why = "Алиса слушает; text — то, что она распознала (final — фраза закончена и пойдёт в облако)."
				case strings.Contains(text, "LISTENING"):
					why = "Алиса услышала «Алиса» и слушает команду."
				case strings.Contains(text, "BUSY"):
					why = "Фраза ушла в облако, Алиса думает; когда ответ придёт, в нём будут директивы (что делать и что сказать)."
				case strings.Contains(text, "SPEAKING"):
					why = "Алиса говорит."
				case strings.Contains(text, "IDLE"):
					why = "Диалог закончен."
				}
			case strings.HasPrefix(text, "Remoting"):
				lvl = journal.Trace
			case strings.Contains(text, "нет соединения"), strings.Contains(text, "закрыто"):
				lvl = journal.Warn
			}
			d.j.Log(lvl, "aliced", text, why, nil)
		},
	}
	go o.Run(ctx)
}

// platformBluetooth передаёт компонентам «включи / выключи блютуз» (наушники отключаются и не подключаются сами).
func (d *daemon) platformBluetooth(on bool) {
	d.mu.Lock()
	comps := d.comps
	d.mu.Unlock()
	for _, c := range comps {
		if b, ok := c.obj.(bluetoothSwitch); ok {
			b.PlatformBluetooth(on)
		}
	}
}
