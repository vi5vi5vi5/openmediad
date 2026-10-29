package daemon

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"openmediad/internal/core/buildinfo"
	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/journal"
)

func (d *daemon) status(detail bool) ctlsock.Status {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	d.mu.Lock()
	cfg, comps := d.cfg, d.comps
	d.mu.Unlock()
	st := ctlsock.Status{
		Version: buildinfo.Get().Line(), PID: os.Getpid(), Mode: cfg.General.Mode, UptimeS: int64(timeSince(d.started).Seconds()),
		Config: d.opt.ConfigPath, Warnings: cfg.Warnings, Connections: d.proxy.NumConns(), LogLevel: d.j.Level(),
		HeapKB: ms.HeapAlloc >> 10, Goroutines: runtime.NumGoroutine(), Components: d.components(),
	}
	if comps == nil { // компоненты ещё подключаются: автомат собирается, его поля не читаем
		return st
	}
	st.Music, st.Voice, st.Sounds = !d.m.NoMusic, d.m.Voice != nil, d.m.File != nil
	if detail {
		st.Players = d.m.Snapshot()
		st.Details = map[string]any{}
		for _, c := range comps {
			if s, ok := c.obj.(debugStater); ok {
				st.Details[c.name] = s.DebugState()
			}
		}
	}
	return st
}

// debugState — снимок для отладочной страницы.
func (d *daemon) debugState() any {
	st := d.status(true)
	mode := "openmediad играет сам: "
	var own []string
	if st.Music {
		own = append(own, "музыку Яндекса")
	}
	if st.Voice {
		own = append(own, "голос Алисы")
	}
	if st.Sounds {
		own = append(own, "звонки и системные звуки")
	}
	if len(own) == 0 {
		mode = "прозрачный посредник: всё играет штатный mediad"
	} else {
		mode += strings.Join(own, ", ") + "; остальное играет штатный mediad"
	}
	return map[string]any{
		"mode":        map[string]any{"режим": mode},
		"connections": st.Connections,
		"players":     st.Players,
		"components":  st.Details, // состояние каждого компонента по его имени
	}
}

// handle — команды CLI через сокет.
func (d *daemon) handle(ctx context.Context, req ctlsock.Request, send func(any) error) error {
	switch req.Cmd {
	case "status":
		return send(d.status(req.Detail))
	case "logs":
		es := d.j.Since(req.Since, req.Limit)
		return send(filterLevel(es, req.Level))
	case "follow":
		ch, unsub := d.j.Subscribe()
		defer unsub()
		for _, e := range filterLevel(d.j.Since(req.Since, req.Limit), req.Level) {
			if err := send(e); err != nil {
				return nil
			}
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case e := <-ch:
				if journal.Rank(e.Lvl) < journal.Rank(req.Level) {
					continue
				}
				if err := send(e); err != nil {
					return nil
				}
			}
		}
	case "reload":
		msg, err := d.reload()
		if err != nil {
			return err
		}
		return send(msg)
	}
	return fmt.Errorf("неизвестная команда %q", req.Cmd)
}

func filterLevel(es []journal.Entry, lvl string) []journal.Entry {
	if lvl == "" {
		return es
	}
	out := es[:0:0]
	for _, e := range es {
		if journal.Rank(e.Lvl) >= journal.Rank(lvl) {
			out = append(out, e)
		}
	}
	return out
}

// reload перечитывает настройки и применяет то, что можно на ходу: уровень журнала, маршруты, приглушение. Остальное
// (компоненты, режим, размер журнала) — после перезапуска; об этом говорит ответ.
func (d *daemon) reload() (string, error) {
	cfg, err := config.LoadFile(d.opt.ConfigPath)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	old := d.cfg
	d.cfg = cfg
	d.mu.Unlock()
	d.j.SetLevel(cfg.General.LogLevel)
	d.rt.set(cfg.Route)
	d.m.SetDuck(cfg.Focus.DuckGain)
	var restart []string
	if old.General.Mode != cfg.General.Mode {
		restart = append(restart, "general.mode")
	}
	if old.General.LogEntries != cfg.General.LogEntries || old.General.LogFile != cfg.General.LogFile {
		restart = append(restart, "журнал")
	}
	if old.Speaker != cfg.Speaker {
		restart = append(restart, "speaker")
	}
	for _, c := range d.components() {
		if cfg.Enabled(c.Name) != c.Enabled {
			restart = append(restart, "компонент "+c.Name)
		}
	}
	for _, w := range cfg.Warnings {
		d.j.Log(journal.Warn, "system", "настройки: "+w, "", nil)
	}
	if len(restart) > 0 {
		return "настройки перечитаны; после openmediad restart применится: " + strings.Join(restart, ", "), nil
	}
	return "настройки перечитаны и применены", nil
}
