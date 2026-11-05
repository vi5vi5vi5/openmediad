package ledring

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/led"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/clockled"
	"openmediad/internal/libs/ringled"
)

// Component — подсветка кольца: следит за панелью индикации (core/led), рисует по ней кадр и удерживает его на кольце.
type Component struct {
	panel *led.Panel
	logf  func(string, ...any)
	// dev, anim подменяются в тестах
	open func() (ringled.Device, error)
	anim func() ringled.Anim

	mu     sync.Mutex
	cfg    Config
	colors map[string]led.Color
	state  led.State
	an     animator      // время жизни вспышки
	shown  ringled.Frame // кадр вспышки на полной яркости: после паузы панель уже пуста, а гаснуть плавно надо именно им

	hub        *control.Hub                     // ядро: события плееров (пауза, перемотка); nil — без анимаций
	unsub      func()                           // отписка от событий
	patterns   map[control.Cmd]*ringled.Pattern // штатные анимации плеера из прошивки
	patternDir string                           // каталог анимаций (в тестах подменяется)
	fx         *effect                          // анимация, которая играет сейчас
	fxLog      []string                         // последние решения по анимациям (для разбора)
	wake       chan struct{}                    // разбудить animate: началась анимация

	// дисплей с часами: анимации паузы и перемотки (displayfx.go)
	openClock  func() (clockled.Device, error) // nil — дисплея нет (в тестах подменяется)
	clockDir   string                          // каталог анимаций дисплея (в тестах подменяется)
	clock      clockled.Device
	player     *clockled.Player
	clockAnims map[control.Cmd]*clockled.Anim // штатные анимации дисплея из прошивки
	curAnim    func() ringled.Anim            // что прошивка показывает сейчас (по её логу)
	ctx        context.Context
	env        *registry.Env
	// holdSpectrum убирает спектр музыки с дисплея и возвращает функцию, которая его возвращает; nil — службы display-spectrum нет
	holdSpectrum func() func()

	dev    ringled.Device
	keeper *ringled.Keeper
	stop   func()
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newComponent(env *registry.Env, cfg any) (any, error) {
	c := cfg.(*Config)
	if err := c.validate(); err != nil {
		return nil, err
	}
	p, ok := registry.Use[*led.Panel](env, "led")
	if !ok {
		return nil, fmt.Errorf("led-ring: нет панели индикации")
	}
	colors, _ := parseColors(c.Colors)
	hub, _ := registry.Use[*control.Hub](env, "control")
	return &Component{
		panel: p, logf: env.Logf("led"), cfg: *c, colors: colors, hub: hub, env: env,
		open:      func() (ringled.Device, error) { return ringled.OpenFB(ringled.FBPath) },
		openClock: func() (clockled.Device, error) { return clockled.OpenSysfs() },
	}, nil
}

// Start открывает кольцо и дисплей и подписывается на панель и на события плееров. Каждое устройство само по себе: нет /dev/fb0 (не Мини 3) — нет
// кольца, но дисплей работает, и наоборот. Нет обоих — просто сообщает об этом.
func (c *Component) Start(ctx context.Context) error {
	ringDev, ringErr := c.open()
	var clockDev clockled.Device
	clockErr := errNoDisplay
	if c.openClock != nil && c.config().Animations {
		clockDev, clockErr = c.openClock()
	}
	if ringErr != nil && clockErr != nil {
		c.logf("подсветка не работает: кольцо — %v (нужна Станция Мини 3, устройство %s), дисплей — %v", ringErr, ringled.FBPath, clockErr)
		return nil
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.ctx = ctx
	c.wake = make(chan struct{}, 1)
	anim := c.anim
	if anim == nil {
		w := &ringled.Watcher{}
		anim = w.Current
		c.wg.Add(1)
		go func() { defer c.wg.Done(); w.Run(ctx) }()
	}
	c.curAnim = anim
	if ringErr == nil {
		c.dev = ringDev
		c.keeper = &ringled.Keeper{Dev: ringDev, Anim: anim, Logf: c.logf}
		c.wg.Add(2)
		go func() { defer c.wg.Done(); c.keeper.Run(ctx) }()
		go func() { defer c.wg.Done(); c.animate(ctx) }()
		c.stop = c.panel.Watch(c.onPanel)
	} else {
		c.logf("кольцо логотипа не работает: %v (устройство %s)", ringErr, ringled.FBPath)
	}
	if clockErr == nil {
		c.clock = clockDev
		c.player = &clockled.Player{Dev: clockDev, Logf: c.logf}
		c.loadClockAnims()
		if c.holdSpectrum == nil && c.env != nil { // служба другого компонента; к запуску модулей все компоненты уже созданы
			c.holdSpectrum, _ = registry.Use[func() func()](c.env, spectrumHoldService)
		}
	} else if c.openClock != nil && c.config().Animations {
		c.logf("дисплей с часами не работает: %v", clockErr)
	}
	if c.hub != nil {
		if c.config().RingAnimations {
			c.loadPatterns()
		}
		c.unsub = c.hub.Subscribe(c.onEvent)
	}
	return nil
}

// errNoDisplay — дисплей не запрашивался (отключён в настройках или не передан компоненту).
var errNoDisplay = fmt.Errorf("дисплей выключен в настройках (animations = false)")

// Stop отдаёт кольцо прошивке и возвращает дисплею то, что на нём было.
func (c *Component) Stop() {
	if c.stop != nil {
		c.stop()
	}
	if c.unsub != nil {
		c.unsub()
		c.unsub = nil
	}
	if c.player != nil {
		c.player.Close()
	}
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
		c.cancel = nil
	}
	if cl, ok := c.dev.(interface{ Close() error }); ok {
		cl.Close()
	}
	if cl, ok := c.clock.(interface{ Close() error }); ok {
		cl.Close()
	}
}

// onPanel — состояние панели изменилось. Новый источник или выход зажигают кольцо заново на hold секунд; когда показывать нечего, оно плавно гаснет
// тем, что горело. Если что-то только убавилось (пауза: источник замолчал, а выход ещё не закрылся), кадр не меняется и не зажигается заново.
func (c *Component) onPanel(s led.State) {
	now := time.Now()
	c.mu.Lock()
	prev := c.state
	c.state = s
	frame := frameFor(c.cfg, c.colors, s)
	added := s.Source != nil && !sameEntry(prev.Source, s.Source) || s.Output != nil && !sameEntry(prev.Output, s.Output)
	switch {
	case frame.IsZero():
		c.an.Stop(now, c.keeper != nil && c.keeper.Status().Active)
	case added || !c.an.Active():
		c.shown = frame
		c.an.Trigger(now)
	}
	c.mu.Unlock()
	c.step(now)
}

func sameEntry(a, b *led.Entry) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name
}

// animate двигает вспышку и анимации плеера: раз в 50 мс пересчитывает яркость, пока кольцо горит или гаснет, а пока играет анимация прошивки
// (её кадры по 20 мс), — раз в 20 мс.
func (c *Component) animate(ctx context.Context) {
	for {
		d := 50 * time.Millisecond
		if c.effectActive() {
			d = 20 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-time.After(d):
			c.step(time.Now())
		}
	}
}

// step собирает кадр из состояния панели, настроек, уровня вспышки и анимации плеера и отдаёт его хранителю кольца.
func (c *Component) step(now time.Time) {
	if c.keeper == nil {
		return
	}
	c.mu.Lock()
	if f, ok := c.effectFrame(now); ok { // анимация плеера важнее вспышки: идёт поверх неё, вспышка продолжится после
		c.mu.Unlock()
		c.keeper.Show(f)
		return
	}
	if !c.an.Active() {
		c.mu.Unlock()
		c.keeper.Release() // вспышка отменена или кончилась: кольцо прошивке (если оно ещё наше)
		return
	}
	cfg, base := c.cfg, c.shown
	lvl := c.an.Level(now, cfg.Hold, cfg.Fade, c.keeper.Status().Active)
	c.mu.Unlock()
	c.keeper.Set(scaleFrame(base, lvl))
}

// render применяет изменившиеся настройки к тому, что сейчас горит.
func (c *Component) render() { c.step(time.Now()) }

// colorOf — цвет записи: пользовательский из настроек, иначе предложенный самим модулем; нет ни того ни другого — не горит.
func colorOf(e *led.Entry, colors map[string]led.Color) led.Color {
	if e == nil {
		return led.Color{}
	}
	if c, ok := colors[e.Name]; ok {
		return c
	}
	return e.Default
}

// frameFor — кадр кольца: источник в своей зоне, выход в своей (если зоны совпали, выход главнее).
func frameFor(cfg Config, colors map[string]led.Color, s led.State) (f ringled.Frame) {
	if c := colorOf(s.Source, colors); !c.IsZero() {
		f[cfg.SourceZone] = c.Scale(uint8(cfg.SourceBrightness))
	}
	if c := colorOf(s.Output, colors); !c.IsZero() {
		f[cfg.OutputZone] = c.Scale(uint8(cfg.OutputBrightness))
	}
	return f
}

// Reload применяет изменённые настройки на ходу; перезапуск демона не нужен ни для чего.
func (c *Component) Reload(newCfg any) (restart []string) {
	n, ok := newCfg.(*Config)
	if !ok || n == nil {
		return nil
	}
	if err := n.validate(); err != nil {
		c.logf("новые настройки подсветки отвергнуты: %v", err)
		return nil
	}
	colors, _ := parseColors(n.Colors)
	c.mu.Lock()
	c.cfg, c.colors = *n, colors
	if f := frameFor(c.cfg, c.colors, c.state); !f.IsZero() { // новые зоны, цвета и яркость применяются к тому, что горит
		c.shown = f
	}
	c.mu.Unlock()
	c.render()
	return nil
}

// StatusLine — строка для `openmediad status`.
func (c *Component) StatusLine() string {
	if c.keeper == nil {
		return "кольцо недоступно (нет /dev/fb0)"
	}
	c.mu.Lock()
	cfg, colors, s, ph := c.cfg, c.colors, c.state, c.an.phase
	c.mu.Unlock()
	var parts []string
	describe := func(role string, zone int, e *led.Entry) {
		if e == nil {
			return
		}
		if col := colorOf(e, colors); !col.IsZero() {
			parts = append(parts, fmt.Sprintf("%s %s: зона %d %s", role, e.Name, zone, col.Hex()))
		}
	}
	describe("источник", cfg.SourceZone, s.Source)
	describe("выход", cfg.OutputZone, s.Output)
	if len(parts) == 0 {
		return "ничего не горит"
	}
	state := map[phase]string{phaseIdle: "погасло", phaseHolding: "горит", phaseFading: "гаснет"}[ph]
	if st := c.keeper.Status(); ph == phaseHolding && st.Owned && !st.Active {
		state = "горит (кольцо сейчас у прошивки, Алиса)"
	}
	return state + ": " + strings.Join(parts, "; ")
}

// DebugState — состояние для `status -v`.
func (c *Component) DebugState() any {
	c.mu.Lock()
	cfg, colors := c.cfg, c.colors
	c.mu.Unlock()
	st := map[string]any{"colors": formatColors(colors), "source_zone": cfg.SourceZone, "output_zone": cfg.OutputZone, "hold": cfg.Hold.String(), "fade": cfg.Fade.String()}
	if c.keeper != nil {
		ks := c.keeper.Status()
		st["owned"], st["active"], st["yields"] = ks.Owned, ks.Active, ks.Yields
		var frame []string
		for _, z := range ks.Want {
			frame = append(frame, z.Hex())
		}
		st["frame"] = frame
		var trace []string
		for _, e := range c.keeper.Trace() {
			trace = append(trace, e.At.Format("15:04:05.000")+" "+e.What+" "+briefFrame(e.Frame))
		}
		st["trace"] = trace
	}
	c.mu.Lock()
	st["animations_log"] = append([]string(nil), c.fxLog...)
	st["spectrum_hold"] = c.holdSpectrum != nil // служба display-spectrum найдена: на время анимации спектр музыки убирается
	c.mu.Unlock()
	return st
}

// briefFrame — кадр коротко для разбора: «всё #e800f3», «z3=#00ff40 z0=#0060ff» или «чёрный».
func briefFrame(f ringled.Frame) string {
	if f.IsZero() {
		return "чёрный"
	}
	same := true
	for _, z := range f {
		same = same && z == f[0]
	}
	if same {
		return "всё " + f[0].Hex()
	}
	var parts []string
	for i, z := range f {
		if !z.IsZero() {
			parts = append(parts, fmt.Sprintf("z%d=%s", i, z.Hex()))
		}
	}
	return strings.Join(parts, " ")
}
