package ledring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
	"openmediad/internal/core/led"
	"openmediad/internal/libs/ringled"
)

// Файлы «прошивки» для тестов: красный, потом синий и тёмный хвост, который нам ни к чему (как у штатной паузы).
func writePatterns(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	row := func(c string, ms int) string {
		return c + " " + c + " " + c + " " + c + " " + c + " " + c + " " + itoa(ms) + "\n"
	}
	files := map[string]string{
		ringled.PatternPause:    row("ff0000", 80) + row("0000ff", 80) + row("000000", 5000),
		ringled.PatternContinue: row("00ff00", 80) + row("00aa00", 80),
		ringled.PatternNext:     row("101010", 80) + row("202020", 80),
		ringled.PatternPrevious: row("303030", 80) + row("404040", 80),
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func full(c led.Color) ringled.Frame {
	var f ringled.Frame
	for i := range f {
		f[i] = c
	}
	return f
}

type fxRig struct {
	c    *Component
	p    *led.Panel
	ring *fakeRing
	hub  *control.Hub
	busy atomic.Bool // прошивка рисует свою анимацию
}

func startedFx(t *testing.T, edit func(*Config)) *fxRig {
	t.Helper()
	cfg := defaults()
	cfg.Fade = 30 * time.Millisecond
	cfg.RingAnimations = true // эти тесты про кольцо; по умолчанию оно выключено, а анимации идут на дисплее (displayfx_test.go)
	cfg.Animations = false
	if edit != nil {
		edit(cfg)
	}
	colors, _ := parseColors(cfg.Colors)
	g := &fxRig{ring: &fakeRing{}, p: led.NewPanel(), hub: &control.Hub{}}
	g.hub.Add("spotify", controltest.NewPlayer(controltest.Playing()))
	g.c = &Component{panel: g.p, logf: func(string, ...any) {}, cfg: *cfg, colors: colors, hub: g.hub, patternDir: writePatterns(t),
		open: func() (ringled.Device, error) { return g.ring, nil },
		anim: func() ringled.Anim {
			if g.busy.Load() {
				return ringled.Anim{Ring: []string{"alice_thinking.led"}, At: time.Now()}
			}
			return ringled.Anim{}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); g.c.Stop() })
	if err := g.c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return g
}

// Событие плеера через ядро рисует на кольце штатную анимацию из файла прошивки, а потом отдаёт кольцо: чёрный хвост файла не нужен.
func TestPlayerEventPlaysTheFirmwareAnimation(t *testing.T) {
	g := startedFx(t, nil)
	g.hub.Do(control.Pause, control.Headphones)
	waitFrame(t, g.ring, full(led.Color{R: 255}))
	waitFrame(t, g.ring, full(led.Color{B: 255}))
	waitFrame(t, g.ring, ringled.Frame{})
	time.Sleep(60 * time.Millisecond)
	if g.c.keeper.Status().Owned {
		t.Fatal("после анимации кольцо не отдано прошивке")
	}
	if g.c.effectActive() {
		t.Fatal("анимация считается играющей после конца")
	}
}

func TestEachEventHasItsOwnAnimation(t *testing.T) {
	for _, tc := range []struct {
		cmd  control.Cmd
		want ringled.Frame
	}{
		{control.Resume, full(led.Color{G: 255})},
		{control.Next, full(led.Color{R: 0x10, G: 0x10, B: 0x10})},
		{control.Prev, full(led.Color{R: 0x30, G: 0x30, B: 0x30})},
	} {
		g := startedFx(t, nil)
		g.c.onEvent(control.Event{Cmd: tc.cmd, From: control.Headphones, Player: "spotify"})
		waitFrame(t, g.ring, tc.want)
	}
}

// Паузу из приложения ядро объявляет с источником App, и анимация играет так же.
func TestEventFromTheAppPlaysToo(t *testing.T) {
	g := startedFx(t, nil)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.App, Player: "spotify"})
	waitFrame(t, g.ring, full(led.Color{R: 255}))
}

// Когда прошивка рисует анимацию сама (короткая команда, ответ облака, команда усыновлённому плееру), поверх неё не рисуем.
func TestNothingIsDrawnWhereThePlatformDrawsItself(t *testing.T) {
	g := startedFx(t, nil)
	for _, o := range []control.Origin{control.FastVoice, control.Cloud, control.Virtual} {
		g.c.onEvent(control.Event{Cmd: control.Pause, From: o, Player: "spotify"})
	}
	time.Sleep(250 * time.Millisecond)
	g.ring.mu.Lock()
	n := len(g.ring.writes)
	g.ring.mu.Unlock()
	if n != 0 {
		t.Fatalf("на кольцо писали %d раз, хотя анимацию рисует прошивка", n)
	}
}

func TestAnimationsCanBeSwitchedOff(t *testing.T) {
	g := startedFx(t, func(c *Config) { c.RingAnimations = false })
	g.hub.Do(control.Pause, control.Headphones)
	time.Sleep(250 * time.Millisecond)
	if g.ring.frame() != (ringled.Frame{}) || g.c.effectActive() {
		t.Fatal("анимации выключены, а кольцо играет")
	}
}

func TestAnimationBrightness(t *testing.T) {
	g := startedFx(t, func(c *Config) { c.AnimationBrightness = 128 })
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if f := g.ring.frame(); f[0].R >= 127 && f[0].R <= 129 && f[0].G == 0 {
			return
		}
	}
	t.Fatalf("яркость анимации 128 из 255: кольцо %+v", g.ring.frame())
}

// Анимация идёт поверх вспышки источника; когда она кончилась, вспышка продолжается.
func TestAnimationPlaysOverTheFlashAndTheFlashContinues(t *testing.T) {
	g := startedFx(t, func(c *Config) { c.Hold = 5 * time.Second })
	g.p.On(led.Source, "spotify-soloist", green)
	flash := ringled.Frame{3: green.Scale(60)}
	waitFrame(t, g.ring, flash)
	g.hub.Do(control.Pause, control.Headphones)
	waitFrame(t, g.ring, full(led.Color{R: 255}))
	waitFrame(t, g.ring, flash)
}

// Прошивка занята своей анимацией (Алиса думает после жеста): ждём кольцо и играем, когда оно освободилось, а не молча пропускаем.
func TestAnimationWaitsForTheRingFromTheFirmware(t *testing.T) {
	g := startedFx(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Next, From: control.Speaker, Player: "spotify"})
	time.Sleep(200 * time.Millisecond)
	if g.ring.frame() != (ringled.Frame{}) {
		t.Fatalf("рисуем поверх анимации прошивки: %+v", g.ring.frame())
	}
	g.busy.Store(false)
	waitFrame(t, g.ring, full(led.Color{R: 0x10, G: 0x10, B: 0x10}))
	waitFrame(t, g.ring, full(led.Color{R: 0x20, G: 0x20, B: 0x20}))
	waitFrame(t, g.ring, ringled.Frame{})
}

// Нет файлов прошивки (не Мини 3 или другая версия): компонент работает, анимаций просто нет.
func TestMissingFirmwareFilesAreNotFatal(t *testing.T) {
	cfg := defaults()
	cfg.RingAnimations = true
	ring := &fakeRing{}
	hub := &control.Hub{}
	var logged int32
	c := &Component{panel: led.NewPanel(), logf: func(string, ...any) { atomic.AddInt32(&logged, 1) }, cfg: *cfg, hub: hub, patternDir: t.TempDir(),
		open: func() (ringled.Device, error) { return ring, nil }, anim: func() ringled.Anim { return ringled.Anim{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	time.Sleep(100 * time.Millisecond)
	if ring.frame() != (ringled.Frame{}) {
		t.Fatal("нарисовали анимацию без файла")
	}
	if atomic.LoadInt32(&logged) == 0 {
		t.Fatal("в журнале нет объяснения, почему анимаций нет")
	}
}

func TestAnimationSettingsValidate(t *testing.T) {
	c := defaults()
	c.AnimationBrightness = 300
	if err := c.validate(); err == nil {
		t.Fatal("яркость анимации 300 принята")
	}
	c.AnimationBrightness = 0
	if err := c.validate(); err != nil {
		t.Fatalf("яркость 0 допустима: %v", err)
	}
}

// Прошивка держит кольцо дольше прежних полутора секунд (слушает, думает, отвечает): анимация ждёт и проигрывается целиком, а не начинает
// отсчёт вслепую и не теряет начало под чужой анимацией.
func TestAnimationWaitsLongerThanOneAndAHalfSeconds(t *testing.T) {
	g := startedFx(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Next, From: control.Voice, Player: "spotify"})
	time.Sleep(1700 * time.Millisecond)
	if g.ring.frame() != (ringled.Frame{}) {
		t.Fatalf("рисуем поверх анимации прошивки: %+v", g.ring.frame())
	}
	g.busy.Store(false)
	waitFrame(t, g.ring, full(led.Color{R: 0x10, G: 0x10, B: 0x10})) // с самого первого кадра
	waitFrame(t, g.ring, full(led.Color{R: 0x20, G: 0x20, B: 0x20}))
	waitFrame(t, g.ring, ringled.Frame{})
}

// Кольцо так и не освободилось: анимация отбрасывается, а не всплывает потом ни к селу ни к городу.
func TestAnimationIsDroppedIfTheRingNeverFreesUp(t *testing.T) {
	defer func(old time.Duration) { maxEffectWait = old }(maxEffectWait)
	maxEffectWait = 200 * time.Millisecond
	g := startedFx(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.Voice, Player: "spotify"})
	time.Sleep(500 * time.Millisecond)
	g.busy.Store(false)
	time.Sleep(300 * time.Millisecond)
	if g.ring.frame() != (ringled.Frame{}) || g.c.effectActive() {
		t.Fatalf("опоздавшая анимация всплыла: %+v", g.ring.frame())
	}
	st, ok := g.c.DebugState().(map[string]any)
	if !ok {
		t.Fatalf("DebugState: %T", g.c.DebugState())
	}
	joined := strings.Join(st["animations_log"].([]string), "\n")
	if !strings.Contains(joined, "отброшена") || !strings.Contains(joined, "кольцо: ждём") {
		t.Fatalf("в журнале разбора нет решений: %s", joined)
	}
}

// Для разбора «почему не загорелось»: решения по анимациям и последние события хранителя видны в состоянии.
func TestDebugStateShowsWhatHappenedToTheRing(t *testing.T) {
	g := startedFx(t, nil)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.FastVoice, Player: "spotify"})
	waitFrame(t, g.ring, full(led.Color{R: 255}))
	var st map[string]any
	var log string
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		st = g.c.DebugState().(map[string]any)
		if log = strings.Join(st["animations_log"].([]string), "\n"); strings.Contains(log, "кольцо у нас через") {
			break
		}
	}
	for _, want := range []string{"пауза (кнопка наушников): кольцо: ждём", "кольцо у нас через", "пауза (короткая команда): кольцо: пропущено"} {
		if !strings.Contains(log, want) {
			t.Errorf("в журнале нет %q:\n%s", want, log)
		}
	}
	trace := strings.Join(st["trace"].([]string), "\n")
	if !strings.Contains(trace, "take") || !strings.Contains(trace, "write всё #ff0000") {
		t.Errorf("в трассе хранителя нет захвата и записи кадра:\n%s", trace)
	}
}
