package app

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"openmediad/internal/audio"
	"openmediad/internal/config"
	"openmediad/internal/engine"
)

// Sink — выход звука (динамики, позже наушники и сеть).
type Sink interface {
	Name() string
	Available() bool
	Open(f audio.Format) (audio.Output, error)
}

// router выбирает выход для класса по настройке [route]: первый доступный из списка. Маршруты меняются на ходу (reload);
// выходы компонентов (Bluetooth) добавляются при запуске компонентов.
type router struct {
	mu     sync.RWMutex
	sinks  map[string]Sink
	routes atomic.Pointer[config.Route]
}

// add подключает выход компонента.
func (rt *router) add(s Sink) {
	rt.mu.Lock()
	rt.sinks[s.Name()] = s
	rt.mu.Unlock()
}

func (rt *router) sink(name string) (Sink, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	s, ok := rt.sinks[name]
	return s, ok
}

func newRouter(r config.Route, sinks ...Sink) *router {
	rt := &router{sinks: map[string]Sink{}}
	for _, s := range sinks {
		rt.sinks[s.Name()] = s
	}
	rt.set(r)
	return rt
}

func (rt *router) set(r config.Route) { rt.routes.Store(&r) }

func (rt *router) list(c engine.Class) []string {
	r := rt.routes.Load()
	switch c {
	case engine.Voice:
		return r.Voice
	case engine.Alert:
		return r.Alert
	}
	return r.Music
}

// better — выход name (только что ставший доступным) стоит в маршруте класса раньше любого другого доступного выхода:
// звучащие потоки класса стоит перевести в него.
func (rt *router) better(c engine.Class, name string) bool {
	for _, n := range rt.list(c) {
		if n == name {
			return true
		}
		if s, ok := rt.sink(n); ok && s.Available() {
			return false
		}
	}
	return false
}

// Output открывает первый доступный выход класса; если ни один не открылся — ошибка со всеми причинами.
func (rt *router) Output(c engine.Class, f audio.Format) (audio.Output, error) {
	var errs []string
	for _, name := range rt.list(c) {
		s, ok := rt.sink(name)
		if !ok {
			continue
		}
		if !s.Available() {
			errs = append(errs, name+": недоступен")
			continue
		}
		out, err := s.Open(f)
		if err == nil {
			return out, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", name, err))
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("для %s нет выходов", c)
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}
