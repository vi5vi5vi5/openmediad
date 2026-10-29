package daemon

import (
	"hash/fnv"
	"sync"
	"time"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/explain"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/shim"
	"openmediad/internal/core/quasar/wire"
)

// Перехват шины: что делает демон с каждым кадром, который платформа шлёт плееру.

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
