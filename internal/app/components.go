package app

import (
	"context"
	"fmt"

	"openmediad/internal/engine"
	"openmediad/internal/journal"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/registry"
)

// component — компонент сборки в работающем демоне.
type component struct {
	name    string
	enabled bool
	obj     any
	err     error
}

// Интерфейсы, по которым демон подключает созданные компоненты (компонент реализует нужные ему).
type (
	module interface {
		Start(ctx context.Context) error
		Stop()
	}
	shutdowner   interface{ Shutdown() }
	fileAccepter interface{ Accepts(path string) bool }
	debugStater  interface{ DebugState() any }
	// arrivalNotifier — выход, который сообщает, что стал доступен (подключились наушники).
	arrivalNotifier interface{ OnArrival(func()) }
	// bluetoothSwitch — компонент, которому важно «включи / выключи блютуз» платформы.
	bluetoothSwitch interface{ PlatformBluetooth(on bool) }
)

// componentOn — включён ли компонент сборки в настройках.
func (d *daemon) componentOn(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.Enabled(name)
}

// startComponents создаёт включённые компоненты сборки и подключает их: бэкенды — к автомату mediad, модули — запускает.
// Ошибка одного компонента не останавливает демон: он работает без него (а то, что компонент делал, остаётся штатному mediad).
// Модули (отладочный порт и т. п.) запускаются последними, когда автомат уже собран: их запросы видят готовое состояние.
func (d *daemon) startComponents(ctx context.Context) {
	env := &registry.Env{
		Logf: d.j.Logf,
		Services: map[string]any{
			"engine":  d.eng,
			"machine": d.m,
			"journal": d.j,
			"state":   d.debugState,
		},
	}
	comps := []*component{} // не nil: признак «компоненты подключены» для status
	for _, rc := range registry.All() {
		comps = append(comps, &component{name: rc.Name, enabled: d.cfg.Enabled(rc.Name)})
	}
	var modules []*component
	for i, rc := range registry.All() {
		c := comps[i]
		if !c.enabled || rc.New == nil {
			continue
		}
		obj, err := rc.New(env, d.cfg.Components[rc.Name])
		if err != nil {
			c.err = err
			d.j.Log(journal.Warn, "system", fmt.Sprintf("компонент %s не запущен: %v", rc.Name, err), "Движок работает без него; то, что он делал, остаётся штатному mediad.", nil)
			continue
		}
		c.obj = obj
		d.attach(c)
		if _, ok := obj.(module); ok {
			modules = append(modules, c)
		}
	}
	d.mu.Lock()
	d.comps = comps
	d.mu.Unlock()
	for _, c := range modules {
		mo := c.obj.(module)
		if err := mo.Start(ctx); err != nil {
			d.mu.Lock()
			c.err = err
			d.mu.Unlock()
			d.j.Log(journal.Warn, "system", fmt.Sprintf("компонент %s не запустился: %v", c.name, err), "", nil)
			continue
		}
		d.closers = append(d.closers, mo.Stop)
	}
	var on []string
	for _, c := range comps {
		if c.obj != nil {
			on = append(on, c.name)
		}
	}
	d.j.Log(journal.Info, "system", fmt.Sprintf("компоненты: %v; музыка %v, голос %v, звонки и системные звуки %v", on, !d.m.NoMusic, d.m.Voice != nil, d.m.File != nil),
		"Что openmediad играет сам; всё остальное играет штатный mediad.", nil)
}

// attach подключает созданный компонент к автомату mediad по интерфейсам, которые он реализует.
func (d *daemon) attach(c *component) {
	switch b := c.obj.(type) {
	case mediad.Backend:
		d.m.Back, d.m.NoMusic = b, false
	case mediad.VoiceBackend:
		d.m.Voice = b
	case mediad.FileBackend:
		d.m.File = b
		if a, ok := c.obj.(fileAccepter); ok {
			d.m.FileOK = a.Accepts
		}
	}
	if s, ok := c.obj.(Sink); ok { // выход звука (Bluetooth-наушники): маршруты [route] видят его по имени
		d.rt.add(s)
		if n, ok := c.obj.(arrivalNotifier); ok { // наушники подключились — звучащие потоки переходят в них
			name := s.Name()
			n.OnArrival(func() { d.eng.Reroute(func(c engine.Class) bool { return d.rt.better(c, name) }) })
		}
	}
	if s, ok := c.obj.(shutdowner); ok {
		d.closers = append(d.closers, s.Shutdown)
	}
}

// components — копия состояния компонентов (для status).
func (d *daemon) components() []ComponentStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []ComponentStatus
	for _, c := range d.comps {
		cs := ComponentStatus{Name: c.name, Enabled: c.enabled, Running: c.obj != nil && c.err == nil}
		if c.err != nil {
			cs.Error = c.err.Error()
		}
		out = append(out, cs)
	}
	return out
}
