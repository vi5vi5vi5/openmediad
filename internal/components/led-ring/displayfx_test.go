package ledring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
	"openmediad/internal/core/led"
	"openmediad/internal/libs/clockled"
	"openmediad/internal/libs/ringled"
)

// fakeClock — дисплей с часами: время «12:34» до анимации; прошивка может перерисовать его в любой момент.
type fakeClock struct {
	mu     sync.Mutex
	cur    clockled.Frame
	writes int
	closed bool
}

func (d *fakeClock) Read() (clockled.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur, nil
}
func (d *fakeClock) Write(f clockled.Frame) error {
	d.mu.Lock()
	d.cur = f
	d.writes++
	d.mu.Unlock()
	return nil
}
func (d *fakeClock) Close() error          { d.mu.Lock(); d.closed = true; d.mu.Unlock(); return nil }
func (d *fakeClock) frame() clockled.Frame { d.mu.Lock(); defer d.mu.Unlock(); return d.cur }
func (d *fakeClock) nwrites() int          { d.mu.Lock(); defer d.mu.Unlock(); return d.writes }

func segs(vals map[string]uint8) (f clockled.Frame) {
	for n, v := range vals {
		i, ok := clockled.Index(n)
		if !ok {
			panic(n)
		}
		f[i] = v
	}
	return f
}

var clockTime = segs(map[string]uint8{"B0": 0xff, "C0": 0xff, "A1": 0xff, "DH": 0xff, "DL": 0xff}) // «12:34» условно

// writeClockAnims кладёт в каталог «прошивки» сжатые анимации дисплея: пауза и перемотки, по два кадра по 30 мс.
func writeClockAnims(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	mk := func(frames ...map[string]uint8) []byte {
		var raw []byte
		for _, f := range frames {
			fr := segs(f)
			raw = append(raw, 0)
			raw = append(raw, fr[:]...)
			raw = append(raw, 0, 30)
		}
		return w.EncodeAll(raw, nil)
	}
	files := map[string][]byte{
		clockled.AnimPause:    mk(map[string]uint8{"B1": 0xf0, "E2": 0xf0}, map[string]uint8{"B1": 0x70, "E2": 0x70}),
		clockled.AnimNext:     mk(map[string]uint8{"E2": 0xe0}, map[string]uint8{"B2": 0xe0}),
		clockled.AnimPrevious: mk(map[string]uint8{"B1": 0xd0}, map[string]uint8{"E1": 0xd0}),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".binscled.zst"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type dispRig struct {
	c     *Component
	clock *fakeClock
	hub   *control.Hub
	busy  atomic.Bool // прошивка занимает дисплей (спектр музыки, «Алиса думает»)

	holds, releases atomic.Int32 // удержания спектра музыки (служба display-spectrum): взято и возвращено
}

// hold — служба display-spectrum: считает, сколько раз спектр убирали и вернули.
func (g *dispRig) hold() func() {
	g.holds.Add(1)
	var once sync.Once
	return func() { once.Do(func() { g.releases.Add(1) }) }
}

func startedDisplay(t *testing.T, edit func(*Config)) *dispRig {
	t.Helper()
	cfg := defaults() // animations = true, ring_animations = false
	if edit != nil {
		edit(cfg)
	}
	colors, _ := parseColors(cfg.Colors)
	g := &dispRig{clock: &fakeClock{cur: clockTime}, hub: &control.Hub{}}
	g.hub.Add("spotify", controltest.NewPlayer(controltest.Playing()))
	g.c = &Component{panel: led.NewPanel(), logf: func(string, ...any) {}, cfg: *cfg, colors: colors, hub: g.hub,
		clockDir:     writeClockAnims(t),
		holdSpectrum: g.hold,
		open:         func() (ringled.Device, error) { return &fakeRing{}, nil },
		openClock:    func() (clockled.Device, error) { return g.clock, nil },
		anim: func() ringled.Anim {
			if g.busy.Load() {
				return ringled.Anim{Clock: []string{"spectrogram"}}
			}
			return ringled.Anim{Clock: []string{"<Animation>"}}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); g.c.Stop() })
	if err := g.c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return g
}

func waitClock(t *testing.T, d *fakeClock, want clockled.Frame) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(3 * time.Millisecond) {
		if d.frame() == want {
			return
		}
	}
	t.Fatalf("дисплей: %v, ждали %v", d.frame().Lit(), want.Lit())
}

// Пауза кнопкой в наушниках: платформа о ней не знает, поэтому дисплей показывает штатную анимацию паузы (две вертикали вокруг двоеточия), а потом время.
func TestHeadphonePauseShowsThePauseAnimationOnTheDisplay(t *testing.T) {
	g := startedDisplay(t, nil)
	g.hub.Do(control.Pause, control.Headphones)
	waitClock(t, g.clock, segs(map[string]uint8{"B1": 0xf0, "E2": 0xf0}))
	waitClock(t, g.clock, segs(map[string]uint8{"B1": 0x70, "E2": 0x70}))
	waitClock(t, g.clock, clockTime) // время вернулось
	time.Sleep(30 * time.Millisecond)
	if g.clock.frame() != clockTime {
		t.Fatalf("время потеряно: %v", g.clock.frame().Lit())
	}
}

// Перемотка вперёд и назад — свои анимации; из приложения Spotify (App) и кнопкой колонки (облако анимации не присылает) тоже.
func TestSkipsShowTheirOwnAnimations(t *testing.T) {
	for _, tc := range []struct {
		cmd  control.Cmd
		from control.Origin
		want clockled.Frame
	}{
		{control.Next, control.Headphones, segs(map[string]uint8{"E2": 0xe0})},
		{control.Prev, control.App, segs(map[string]uint8{"B1": 0xd0})},
		{control.Next, control.Speaker, segs(map[string]uint8{"E2": 0xe0})},
		{control.Prev, control.Voice, segs(map[string]uint8{"B1": 0xd0})},
		{control.Pause, control.Voice, segs(map[string]uint8{"B1": 0xf0, "E2": 0xf0})}, // длинная фраза: диалог отменён, ответа с анимацией не будет
		{control.Pause, control.App, segs(map[string]uint8{"B1": 0xf0, "E2": 0xf0})},
	} {
		g := startedDisplay(t, nil)
		g.c.onEvent(control.Event{Cmd: tc.cmd, From: tc.from, Player: "spotify"})
		waitClock(t, g.clock, tc.want)
	}
}

// Где прошивка рисует сама, поверх не рисуем: короткая команда, ответ облака, команда усыновлённому плееру, а также пауза кнопкой колонки и фразой
// (локальна, с анимацией). У «продолжить» анимации дисплея нет.
func TestDisplayIsLeftAloneWhereThePlatformDraws(t *testing.T) {
	g := startedDisplay(t, nil)
	for _, e := range []control.Event{
		{Cmd: control.Pause, From: control.FastVoice}, {Cmd: control.Next, From: control.FastVoice}, {Cmd: control.Prev, From: control.Cloud},
		{Cmd: control.Pause, From: control.Virtual}, {Cmd: control.Pause, From: control.Speaker},
		{Cmd: control.Resume, From: control.Headphones}, {Cmd: control.Resume, From: control.App},
	} {
		e.Player = "spotify"
		g.c.onEvent(e)
	}
	time.Sleep(250 * time.Millisecond)
	if g.clock.nwrites() != 0 {
		t.Fatalf("дисплей перерисован %d раз, хотя анимацию рисует прошивка или её нет", g.clock.nwrites())
	}
	log := strings.Join(g.c.DebugState().(map[string]any)["animations_log"].([]string), "\n")
	for _, want := range []string{"пауза (короткая команда): дисплей: пропущено", "продолжить (кнопка наушников): дисплей: у прошивки нет анимации"} {
		if !strings.Contains(log, want) {
			t.Errorf("в журнале нет %q:\n%s", want, log)
		}
	}
}

// Пока на дисплее спектр музыки или «Алиса думает», ждём; освободился — играем. Совсем не освободился — отбрасываем, а не показываем потом.
func TestDisplayWaitsUntilTheFirmwareIsDone(t *testing.T) {
	g := startedDisplay(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Next, From: control.Headphones, Player: "spotify"})
	time.Sleep(150 * time.Millisecond)
	if g.clock.nwrites() != 0 {
		t.Fatal("рисуем поверх спектра")
	}
	g.busy.Store(false)
	waitClock(t, g.clock, segs(map[string]uint8{"E2": 0xe0}))
	waitClock(t, g.clock, clockTime)
}

func TestDisplayAnimationIsDroppedIfTheDisplayStaysBusy(t *testing.T) {
	defer func(old time.Duration) { maxDisplayWait = old }(maxDisplayWait)
	maxDisplayWait = 150 * time.Millisecond
	g := startedDisplay(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	time.Sleep(400 * time.Millisecond)
	g.busy.Store(false)
	time.Sleep(200 * time.Millisecond)
	if g.clock.nwrites() != 0 {
		t.Fatal("опоздавшая анимация всплыла")
	}
	if log := strings.Join(g.c.DebugState().(map[string]any)["animations_log"].([]string), "\n"); !strings.Contains(log, "отброшена") {
		t.Fatalf("в журнале нет решения:\n%s", log)
	}
}

func TestDisplayAnimationsCanBeSwitchedOff(t *testing.T) {
	g := startedDisplay(t, func(c *Config) { c.Animations = false })
	g.hub.Do(control.Pause, control.Headphones)
	time.Sleep(200 * time.Millisecond)
	if g.clock.nwrites() != 0 || g.c.player != nil {
		t.Fatal("анимации выключены, а дисплей в работе")
	}
}

func TestDisplayAnimationBrightness(t *testing.T) {
	g := startedDisplay(t, func(c *Config) { c.AnimationBrightness = 128 })
	g.c.onEvent(control.Event{Cmd: control.Next, From: control.Headphones, Player: "spotify"})
	i, _ := clockled.Index("E2")
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(3 * time.Millisecond) {
		if v := g.clock.frame()[i]; v >= 111 && v <= 114 { // 0xe0 · 128 / 255
			return
		}
	}
	t.Fatalf("яркость 128 из 255: %v", g.clock.frame().Lit())
}

// Нет файлов прошивки (другая версия, не Мини 3): работают остальные возможности, анимаций просто нет.
func TestMissingDisplayFilesAreNotFatal(t *testing.T) {
	var logged int32
	cfg := defaults()
	g := &dispRig{clock: &fakeClock{cur: clockTime}, hub: &control.Hub{}}
	c := &Component{panel: led.NewPanel(), logf: func(string, ...any) { atomic.AddInt32(&logged, 1) }, cfg: *cfg, hub: g.hub, clockDir: t.TempDir(),
		open:      func() (ringled.Device, error) { return &fakeRing{}, nil },
		openClock: func() (clockled.Device, error) { return g.clock, nil },
		anim:      func() ringled.Anim { return ringled.Anim{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	time.Sleep(100 * time.Millisecond)
	if g.clock.nwrites() != 0 || atomic.LoadInt32(&logged) == 0 {
		t.Fatalf("записей %d, сообщений в журнал %d", g.clock.nwrites(), atomic.LoadInt32(&logged))
	}
}

// Дисплей и кольцо независимы: нет кольца (не тот /dev/fb0) — анимации дисплея всё равно идут; а при остановке дисплею возвращают время.
func TestDisplayWorksWithoutTheRingAndStopRestoresTheTime(t *testing.T) {
	g := startedDisplay(t, nil)
	g.c.Stop() // чистим то, что подняли
	cfg := defaults()
	c := &Component{panel: led.NewPanel(), logf: func(string, ...any) {}, cfg: *cfg, hub: g.hub, clockDir: writeClockAnims(t),
		open:      func() (ringled.Device, error) { return nil, os.ErrNotExist },
		openClock: func() (clockled.Device, error) { return g.clock, nil },
		anim:      func() ringled.Anim { return ringled.Anim{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c.onEvent(control.Event{Cmd: control.Next, From: control.Headphones, Player: "spotify"})
	waitClock(t, g.clock, segs(map[string]uint8{"E2": 0xe0}))
	c.Stop()
	if g.clock.frame() != clockTime {
		t.Fatalf("при остановке не вернули время: %v", g.clock.frame().Lit())
	}
}
