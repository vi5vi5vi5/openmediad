package ledring

import (
	"testing"
	"time"

	"openmediad/internal/core/control"
)

// Спектр музыки закрывает дисплей: его убирают сразу, как заказана анимация, и возвращают, только когда она доиграла и время снова на месте.
func TestSpectrumIsHeldWhileTheAnimationPlays(t *testing.T) {
	g := startedDisplay(t, nil)
	g.c.onEvent(control.Event{Cmd: control.Next, From: control.Headphones, Player: "spotify"})
	waitClock(t, g.clock, segs(map[string]uint8{"E2": 0xe0}))
	if g.holds.Load() != 1 || g.releases.Load() != 0 {
		t.Fatalf("пока играет анимация: убрано %d, возвращено %d (нужно 1 и 0)", g.holds.Load(), g.releases.Load())
	}
	waitClock(t, g.clock, clockTime)
	for end := time.Now().Add(time.Second); g.releases.Load() == 0 && time.Now().Before(end); time.Sleep(3 * time.Millisecond) {
	}
	if g.releases.Load() != 1 {
		t.Fatalf("после анимации спектр не вернули: возвращено %d", g.releases.Load())
	}
}

// Опоздавшая анимация отбрасывается, и спектр при этом возвращается: дисплей не должен остаться без него.
func TestSpectrumIsReturnedWhenTheAnimationIsDropped(t *testing.T) {
	defer func(old time.Duration) { maxDisplayWait = old }(maxDisplayWait)
	maxDisplayWait = 100 * time.Millisecond
	g := startedDisplay(t, nil)
	g.busy.Store(true)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.Headphones, Player: "spotify"})
	for end := time.Now().Add(2 * time.Second); g.releases.Load() == 0 && time.Now().Before(end); time.Sleep(3 * time.Millisecond) {
	}
	if g.holds.Load() != 1 || g.releases.Load() != 1 {
		t.Fatalf("убрано %d, возвращено %d (нужно 1 и 1)", g.holds.Load(), g.releases.Load())
	}
}

// Где анимацию рисует прошивка (или её нет), спектр не трогаем.
func TestSpectrumIsLeftAloneWhenThePlatformDraws(t *testing.T) {
	g := startedDisplay(t, nil)
	g.c.onEvent(control.Event{Cmd: control.Pause, From: control.FastVoice, Player: "spotify"})
	g.c.onEvent(control.Event{Cmd: control.Resume, From: control.Headphones, Player: "spotify"})
	time.Sleep(100 * time.Millisecond)
	if g.holds.Load() != 0 {
		t.Fatalf("спектр убран %d раз без анимации", g.holds.Load())
	}
}
