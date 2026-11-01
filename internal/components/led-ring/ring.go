package ledring

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"openmediad/internal/core/led"
	"openmediad/internal/core/registry"
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
	return &Component{
		panel: p, logf: env.Logf("led"), cfg: *c, colors: colors,
		open: func() (ringled.Device, error) { return ringled.OpenFB(ringled.FBPath) },
	}, nil
}

// Start открывает кольцо и подписывается на панель. Нет /dev/fb0 (не Мини 3) — просто сообщает об этом.
func (c *Component) Start(ctx context.Context) error {
	dev, err := c.open()
	if err != nil {
		c.logf("подсветка кольца не работает: %v (нужна Станция Мини 3, устройство %s)", err, ringled.FBPath)
		return nil
	}
	c.dev = dev
	anim := c.anim
	var w *ringled.Watcher
	if anim == nil {
		w = &ringled.Watcher{}
		anim = w.Current
	}
	c.keeper = &ringled.Keeper{Dev: dev, Anim: anim, Logf: c.logf}
	ctx, c.cancel = context.WithCancel(ctx)
	if w != nil {
		c.wg.Add(1)
		go func() { defer c.wg.Done(); w.Run(ctx) }()
	}
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.keeper.Run(ctx) }()
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.animate(ctx) }()
	c.stop = c.panel.Watch(c.onPanel)
	return nil
}

// Stop отдаёт кольцо прошивке.
func (c *Component) Stop() {
	if c.stop != nil {
		c.stop()
	}
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
		c.cancel = nil
	}
	if cl, ok := c.dev.(interface{ Close() error }); ok {
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

// animate двигает вспышку: раз в 50 мс пересчитывает яркость, пока кольцо горит или гаснет.
func (c *Component) animate(ctx context.Context) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.step(now)
		}
	}
}

// step собирает кадр из состояния панели, настроек и уровня вспышки и отдаёт его хранителю кольца.
func (c *Component) step(now time.Time) {
	if c.keeper == nil {
		return
	}
	c.mu.Lock()
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
	}
	return st
}
