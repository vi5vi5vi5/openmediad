package daemon

import (
	"context"
	"fmt"

	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
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
	// statusLiner — компонент сам пишет строку о себе для `openmediad status`.
	statusLiner interface{ StatusLine() string }
	// directiveHandler — компоненту интересны директивы ответа Алисы (имя директивы; что она значит, знает сам компонент): демон отдаёт
	// ему имя каждой директивы, а смысл знает сам компонент.
	directiveHandler interface{ OnDirective(name string) }
	// dialogLogger — компонент просит писать в журнал реплики диалога с Алисой (в них распознанные фразы, поэтому по умолчанию
	// они в журнал не попадают).
	dialogLogger interface{ LogsDialog() bool }
	// arrivalNotifier — выход, который сообщает, что стал доступен (например, подключились наушники).
	arrivalNotifier interface{ OnArrival(func()) }
)

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
	if s, ok := c.obj.(Sink); ok { // выход звука (динамики, наушники, сеть …): маршруты [route] видят его по имени
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
func (d *daemon) components() []ctlsock.ComponentStatus {
	d.mu.Lock()
	comps := d.comps
	out := make([]ctlsock.ComponentStatus, 0, len(comps))
	for _, c := range comps {
		cs := ctlsock.ComponentStatus{Name: c.name, Enabled: c.enabled, Running: c.obj != nil && c.err == nil}
		if c.err != nil {
			cs.Error = c.err.Error()
		}
		out = append(out, cs)
	}
	d.mu.Unlock()
	for i, c := range comps { // строки компонентов — без блокировки демона: у компонента свои замки
		if s, ok := c.obj.(statusLiner); ok && out[i].Running {
			out[i].Line = s.StatusLine()
		}
	}
	return out
}

// dialogLogEnabled — хоть один включённый компонент просит писать реплики диалога с Алисой в журнал.
func (d *daemon) dialogLogEnabled() bool {
	d.mu.Lock()
	comps := d.comps
	d.mu.Unlock()
	for _, c := range comps {
		if l, ok := c.obj.(dialogLogger); ok && l.LogsDialog() {
			return true
		}
	}
	return false
}
