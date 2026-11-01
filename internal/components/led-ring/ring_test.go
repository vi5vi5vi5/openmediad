package ledring

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"openmediad/internal/core/led"
	"openmediad/internal/libs/ringled"
)

type fakeRing struct {
	mu     sync.Mutex
	cur    ringled.Frame
	closed bool
	writes []ringled.Frame
}

func (r *fakeRing) Read() (ringled.Frame, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.cur, nil }
func (r *fakeRing) Write(f ringled.Frame) error {
	r.mu.Lock()
	r.cur = f
	r.writes = append(r.writes, f)
	r.mu.Unlock()
	return nil
}
func (r *fakeRing) Close() error         { r.mu.Lock(); r.closed = true; r.mu.Unlock(); return nil }
func (r *fakeRing) frame() ringled.Frame { r.mu.Lock(); defer r.mu.Unlock(); return r.cur }

var (
	yellow = led.Color{R: 255, G: 176, B: 0}
	green  = led.Color{R: 0, G: 255, B: 64}
	blue   = led.Color{R: 0, G: 96, B: 255}
)

func started(t *testing.T, edit func(*Config)) (*Component, *led.Panel, *fakeRing) {
	t.Helper()
	cfg := defaults()
	cfg.Fade = 30 * time.Millisecond // в тестах угасание короткое
	if edit != nil {
		edit(cfg)
	}
	colors, _ := parseColors(cfg.Colors)
	ring := &fakeRing{}
	p := led.NewPanel()
	c := &Component{panel: p, logf: func(string, ...any) {}, cfg: *cfg, colors: colors,
		open: func() (ringled.Device, error) { return ring, nil },
		anim: func() ringled.Anim { return ringled.Anim{} }} // прошивка в покое
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); c.Stop() })
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return c, p, ring
}

func waitFrame(t *testing.T, r *fakeRing, want ringled.Frame) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if r.frame() == want {
			return
		}
	}
	t.Fatalf("кольцо: %+v, ждали %+v", r.frame(), want)
}

func TestSourceAndOutputLightTheirZones(t *testing.T) {
	_, p, ring := started(t, nil)
	var want ringled.Frame
	p.On(led.Source, "yandex-music", yellow)
	want[3] = yellow.Scale(60)
	waitFrame(t, ring, want)
	p.On(led.Output, "bt-out", blue)
	want[0] = blue.Scale(60)
	waitFrame(t, ring, want)
	p.Off(led.Source, "yandex-music") // пауза: источник замолчал, выход ещё не закрылся — кольцо не прыгает
	time.Sleep(60 * time.Millisecond)
	if ring.frame() != want {
		t.Fatalf("пока выход не закрылся, кадр не должен меняться: %+v", ring.frame())
	}
	p.Off(led.Output, "bt-out")
	waitFrame(t, ring, ringled.Frame{}) // гаснет всё вместе
}

func TestUserColorsAndBrightnessOverrideModuleDefaults(t *testing.T) {
	_, p, ring := started(t, func(c *Config) {
		c.Colors = "spotify-soloist=#ff00ff"
		c.SourceBrightness, c.SourceZone = 255, 5
	})
	p.On(led.Source, "spotify-soloist", green) // модуль предлагает зелёный, пользователь задал пурпурный
	var want ringled.Frame
	want[5] = led.Color{R: 255, B: 255}
	waitFrame(t, ring, want)
}

func TestOutputWithoutAnyColorStaysDark(t *testing.T) {
	_, p, ring := started(t, nil)
	p.On(led.Output, "speaker", led.Color{}) // у динамиков нет цвета ни у модуля, ни в настройках
	p.On(led.Source, "yandex-music", yellow)
	var want ringled.Frame
	want[3] = yellow.Scale(60)
	waitFrame(t, ring, want)
	if ring.frame()[0] != (led.Color{}) {
		t.Fatal("зона выхода горит без цвета")
	}
}

func TestSameZoneOutputWins(t *testing.T) {
	cfg := *defaults()
	cfg.SourceZone, cfg.OutputZone = 2, 2
	f := frameFor(cfg, nil, led.State{Source: &led.Entry{Name: "a", Default: yellow}, Output: &led.Entry{Name: "b", Default: blue}})
	if f[2] != blue.Scale(60) {
		t.Fatalf("при общей зоне выход главнее: %+v", f[2])
	}
}

func TestReloadChangesZonesColorsAndBrightnessLive(t *testing.T) {
	c, p, ring := started(t, nil)
	p.On(led.Source, "yandex-music", yellow)
	var first ringled.Frame
	first[3] = yellow.Scale(60)
	waitFrame(t, ring, first)

	n := *defaults()
	n.SourceZone, n.SourceBrightness, n.Colors = 1, 200, "yandex-music=#00ff00"
	if restart := c.Reload(&n); len(restart) != 0 {
		t.Fatalf("подсветке перезапуск не нужен: %v", restart)
	}
	var second ringled.Frame
	second[1] = led.Color{G: 255}.Scale(200)
	waitFrame(t, ring, second)

	bad := *defaults()
	bad.SourceZone = 9
	c.Reload(&bad) // отвергнуто, прежние настройки на месте
	waitFrame(t, ring, second)
}

func TestStopGivesRingBackAndClosesDevice(t *testing.T) {
	c, p, ring := started(t, nil)
	p.On(led.Source, "yandex-music", yellow)
	var want ringled.Frame
	want[3] = yellow.Scale(60)
	waitFrame(t, ring, want)
	c.Stop()
	if ring.frame() != (ringled.Frame{}) {
		t.Fatal("при остановке кольцо не отдано")
	}
	ring.mu.Lock()
	closed := ring.closed
	ring.mu.Unlock()
	if !closed {
		t.Fatal("устройство не закрыто")
	}
	p.On(led.Source, "spotify-soloist", green) // после Stop подсветка молчит
	time.Sleep(30 * time.Millisecond)
	if ring.frame() != (ringled.Frame{}) {
		t.Fatal("после остановки кольцо рисуется")
	}
}

func TestMissingDeviceIsNotFatal(t *testing.T) {
	c := &Component{panel: led.NewPanel(), logf: func(string, ...any) {}, cfg: *defaults(),
		open: func() (ringled.Device, error) { return nil, errors.New("нет /dev/fb0") }}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("без кольца запуск должен проходить: %v", err)
	}
	if !strings.Contains(c.StatusLine(), "недоступно") {
		t.Fatalf("статус: %q", c.StatusLine())
	}
	c.panel.On(led.Source, "x", yellow) // без кольца панель по-прежнему принимает сообщения
	c.Stop()
}

func TestStatusLineDescribesWhatBurns(t *testing.T) {
	c, p, ring := started(t, nil)
	if got := c.StatusLine(); got != "ничего не горит" {
		t.Fatalf("пустой статус: %q", got)
	}
	p.On(led.Source, "spotify-soloist", green)
	p.On(led.Output, "bt-out", blue)
	var want ringled.Frame
	want[3], want[0] = green.Scale(60), blue.Scale(60)
	waitFrame(t, ring, want)
	got := c.StatusLine()
	if !strings.HasPrefix(got, "горит: ") || !strings.Contains(got, "источник spotify-soloist: зона 3 #00ff40") || !strings.Contains(got, "выход bt-out: зона 0 #0060ff") {
		t.Fatalf("статус: %q", got)
	}
	if st, ok := c.DebugState().(map[string]any); !ok || st["owned"] != true {
		t.Fatalf("DebugState: %v", c.DebugState())
	}
}

func TestConfigValidationAndColorParsing(t *testing.T) {
	ok := defaults()
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	ok.Colors = "yandex-music=#ffb000, spotify-soloist=00ff40;bt-out=#0060ff"
	if err := ok.validate(); err != nil {
		t.Fatalf("разделители пробел, запятая, точка с запятой: %v", err)
	}
	for _, edit := range []func(*Config){
		func(c *Config) { c.SourceZone = 6 }, func(c *Config) { c.OutputZone = -1 },
		func(c *Config) { c.SourceBrightness = 256 }, func(c *Config) { c.OutputBrightness = -1 },
		func(c *Config) { c.Colors = "yandex-music" }, func(c *Config) { c.Colors = "=#ffffff" }, func(c *Config) { c.Colors = "a=красный" },
	} {
		c := defaults()
		edit(c)
		if err := c.validate(); err == nil {
			t.Errorf("настройки %+v приняты", c)
		}
	}
	m, _ := parseColors("b=#000001 a=#000002")
	if got := formatColors(m); got != "a=#000002 b=#000001" {
		t.Fatalf("formatColors = %q", got)
	}
}

func (r *fakeRing) history() []ringled.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ringled.Frame(nil), r.writes...)
}

// Главное: кольцо не горит вечно, а вспыхивает на hold и плавно гаснет, хотя музыка продолжает играть.
func TestFlashFadesOutByItselfWhileMusicKeepsPlaying(t *testing.T) {
	c, p, ring := started(t, func(c *Config) { c.Hold, c.Fade = 150*time.Millisecond, 100*time.Millisecond })
	p.On(led.Source, "spotify-soloist", green)
	var want ringled.Frame
	want[3] = green.Scale(60)
	waitFrame(t, ring, want)
	waitFrame(t, ring, ringled.Frame{}) // само погасло
	if p.State().Source == nil {
		t.Fatal("в этом тесте музыка должна продолжать играть")
	}
	if got := c.StatusLine(); !strings.HasPrefix(got, "погасло: ") {
		t.Fatalf("статус после угасания: %q", got)
	}
	// дальше кольцо не трогают: ни записей, ни чтения
	n := len(ring.history())
	time.Sleep(120 * time.Millisecond)
	if len(ring.history()) != n {
		t.Fatal("погасшее кольцо снова записывается")
	}
}

func TestFadeIsGradualNotACut(t *testing.T) {
	_, p, ring := started(t, func(c *Config) { c.Hold, c.Fade = 100*time.Millisecond, 600*time.Millisecond })
	p.On(led.Source, "spotify-soloist", green)
	var want ringled.Frame
	want[3] = green.Scale(60)
	waitFrame(t, ring, want)
	waitFrame(t, ring, ringled.Frame{})
	prev, steps := 1000, 0
	for _, f := range ring.history() {
		g := int(f[3].G)
		if g == 0 || g >= int(want[3].G) {
			continue
		}
		if g > prev {
			t.Fatalf("яркость при угасании растёт: %d после %d", g, prev)
		}
		prev = g
		steps++
	}
	if steps < 3 {
		t.Fatalf("угасание было почти скачком: промежуточных кадров %d (%v)", steps, ring.history())
	}
}

// Новая музыка (после паузы, смена источника) снова зажигает кольцо на hold.
func TestNewMusicLightsTheRingAgain(t *testing.T) {
	_, p, ring := started(t, func(c *Config) { c.Hold, c.Fade = 100*time.Millisecond, 50*time.Millisecond })
	var want ringled.Frame
	want[3] = green.Scale(60)
	p.On(led.Source, "spotify-soloist", green)
	waitFrame(t, ring, want)
	waitFrame(t, ring, ringled.Frame{})
	p.Off(led.Source, "spotify-soloist")       // пауза
	p.On(led.Source, "spotify-soloist", green) // продолжили
	waitFrame(t, ring, want)
	waitFrame(t, ring, ringled.Frame{})
	var yw ringled.Frame
	yw[3] = yellow.Scale(60)
	p.On(led.Source, "yandex-music", yellow) // заиграл другой источник
	waitFrame(t, ring, yw)
}

// Пауза посреди вспышки: кольцо не обрывается, а плавно гаснет.
func TestPauseDuringFlashFadesInsteadOfCutting(t *testing.T) {
	_, p, ring := started(t, func(c *Config) { c.Hold, c.Fade = 5*time.Second, 500*time.Millisecond })
	var want ringled.Frame
	want[3] = green.Scale(60)
	p.On(led.Source, "spotify-soloist", green)
	waitFrame(t, ring, want)
	p.Off(led.Source, "spotify-soloist")
	time.Sleep(120 * time.Millisecond)
	if f := ring.frame(); f == (ringled.Frame{}) || f == want {
		t.Fatalf("через 120 мс после паузы кольцо должно быть между полной яркостью и тьмой: %+v", f)
	}
	waitFrame(t, ring, ringled.Frame{})
}

// hold = "0s" — прежнее поведение: горит, пока музыка играет.
func TestZeroHoldKeepsItLitWhilePlaying(t *testing.T) {
	_, p, ring := started(t, func(c *Config) { c.Hold = 0 })
	var want ringled.Frame
	want[3] = green.Scale(60)
	p.On(led.Source, "spotify-soloist", green)
	waitFrame(t, ring, want)
	time.Sleep(300 * time.Millisecond)
	if ring.frame() != want {
		t.Fatal("при hold = 0 кольцо погасло, пока музыка играет")
	}
	p.Off(led.Source, "spotify-soloist")
	waitFrame(t, ring, ringled.Frame{})
}

func TestHoldAndFadeValidationAndReload(t *testing.T) {
	for _, edit := range []func(*Config){
		func(c *Config) { c.Hold = -time.Second }, func(c *Config) { c.Hold = time.Hour }, func(c *Config) { c.Fade = -1 }, func(c *Config) { c.Fade = time.Minute },
	} {
		c := defaults()
		edit(c)
		if err := c.validate(); err == nil {
			t.Errorf("настройки %+v приняты", c)
		}
	}
	c, p, ring := started(t, func(c *Config) { c.Hold = 0 })
	var want ringled.Frame
	want[3] = green.Scale(60)
	p.On(led.Source, "spotify-soloist", green)
	waitFrame(t, ring, want)
	n := *defaults()
	n.Hold, n.Fade = 50*time.Millisecond, 50*time.Millisecond
	c.Reload(&n) // новое hold применяется к текущей вспышке
	waitFrame(t, ring, ringled.Frame{})
}
