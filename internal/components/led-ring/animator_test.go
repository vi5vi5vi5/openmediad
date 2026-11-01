package ledring

import (
	"math"
	"testing"
	"time"

	"openmediad/internal/core/led"
	"openmediad/internal/libs/ringled"
)

func at[T ~int | ~int64](ms T) time.Time {
	return time.Unix(1000, 0).Add(time.Duration(ms) * time.Millisecond)
}

const (
	hold = 5 * time.Second
	fade = 2 * time.Second
)

func TestIdleAnimatorIsDark(t *testing.T) {
	var a animator
	if a.Active() || a.Level(at(0), hold, fade, false) != 0 {
		t.Fatal("новый аниматор должен быть погашен")
	}
	a.Stop(at(0), false) // остановка погашенного ничего не делает
	if a.Active() {
		t.Fatal("Stop зажёг аниматор")
	}
}

// Вспышка: пока кадр не появился на кольце, отсчёт не идёт; потом hold на полную, потом плавное угасание до нуля.
func TestFlashHoldsThenFadesToZero(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	if got := a.Level(at(100), hold, fade, false); got != 1 {
		t.Fatalf("пока ждём кольцо, уровень %v", got)
	}
	if got := a.Level(at(3000), hold, fade, false); got != 1 {
		t.Fatalf("отсчёт удержания не должен идти, пока кадра нет на кольце: %v", got)
	}
	// кадр появился на кольце в 3500 мс: hold считается отсюда
	if got := a.Level(at(3500), hold, fade, true); got != 1 {
		t.Fatalf("уровень в начале удержания %v", got)
	}
	if got := a.Level(at(3500+4900), hold, fade, true); got != 1 {
		t.Fatalf("до конца удержания уровень %v", got)
	}
	// после hold — угасание: монотонно вниз, плавно, до нуля за fade
	start := 3500 + 5000
	prev := 1.0
	for ms := 100; ms < 2000; ms += 100 {
		got := a.Level(at(start+ms), hold, fade, true)
		if got >= prev || got <= 0 {
			t.Fatalf("через %d мс угасания уровень %v после %v: ждали строго убывающий в (0, 1)", ms, got, prev)
		}
		prev = got
	}
	if got := a.Level(at(start+2000), hold, fade, true); got != 0 || a.Active() {
		t.Fatalf("после fade: уровень %v, активен %v", got, a.Active())
	}
	if got := a.Level(at(start+9000), hold, fade, true); got != 0 {
		t.Fatalf("погасший аниматор снова горит: %v", got)
	}
}

func TestFadeCurveIsSmoothAndEndsAtZero(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Level(at(0), hold, fade, true)           // кадр на кольце
	a.Level(at(5000), hold, fade, true)        // конец удержания → угасание началось
	mid := a.Level(at(6000), hold, fade, true) // середина fade: (1-0,5)² = 0,25
	if math.Abs(mid-0.25) > 0.02 {
		t.Fatalf("середина угасания %v, ждали около 0,25", mid)
	}
}

// Ожидание кольца ограничено: прошивка могла надолго занять его, а удержание всё равно должно когда-то кончиться.
func TestWaitForRingIsBounded(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Level(at(maxLightWait.Milliseconds()-1), hold, fade, false)
	if !a.waiting {
		t.Fatal("ожидание кончилось раньше срока")
	}
	a.Level(at(maxLightWait.Milliseconds()), hold, fade, false)
	if a.waiting {
		t.Fatal("ожидание кольца не ограничено")
	}
	if got := a.Level(at(maxLightWait.Milliseconds()+hold.Milliseconds()+fade.Milliseconds()+10), hold, fade, false); got != 0 {
		t.Fatalf("после hold и fade кольцо должно быть погашено: %v", got)
	}
}

func TestStopWhileHoldingFadesFromFullLevel(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Level(at(0), hold, fade, true)
	a.Level(at(1000), hold, fade, true)
	a.Stop(at(1000), true) // музыку поставили на паузу посреди удержания
	if got := a.Level(at(1000), hold, fade, true); got != 1 {
		t.Fatalf("угасание должно плавно начинаться с текущего уровня, а не с нуля: %v", got)
	}
	if got := a.Level(at(2000), hold, fade, true); !(got > 0 && got < 1) {
		t.Fatalf("середина угасания %v", got)
	}
	if got := a.Level(at(3000), hold, fade, true); got != 0 || a.Active() {
		t.Fatalf("после угасания: %v %v", got, a.Active())
	}
}

func TestStopBeforeItEverLitJustCancels(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Stop(at(10), false)
	if a.Active() {
		t.Fatal("вспышка, которой не было видно, должна отмениться без угасания")
	}
	// кадр уже на кольце, а аниматор этого ещё не заметил: гаснуть надо плавно, а не оставить кольцо гореть
	a.Trigger(at(20))
	a.Stop(at(30), true)
	if !a.Active() || a.Level(at(30), hold, fade, true) != 1 {
		t.Fatal("загоревшаяся вспышка должна плавно гаснуть")
	}
}

func TestRetriggerRestartsHold(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Level(at(0), hold, fade, true)
	a.Level(at(4000), hold, fade, true)
	a.Trigger(at(4000)) // новый трек: вспышка заново
	a.Level(at(4000), hold, fade, true)
	if got := a.Level(at(8000), hold, fade, true); got != 1 {
		t.Fatalf("повторный запуск не продлил удержание: %v", got)
	}
}

func TestZeroHoldMeansWhilePlayingAndZeroFadeIsInstant(t *testing.T) {
	var a animator
	a.Trigger(at(0))
	a.Level(at(0), 0, fade, true)
	if got := a.Level(at(3600000), 0, fade, true); got != 1 {
		t.Fatalf("hold = 0: должно гореть, пока есть что показывать: %v", got)
	}
	a.Stop(at(3600000), true)
	if got := a.Level(at(3600001), 0, 0, true); got != 0 || a.Active() {
		t.Fatalf("fade = 0: гаснет сразу: %v %v", got, a.Active())
	}
}

func TestScaleFrame(t *testing.T) {
	f := ringled.Frame{3: led.Color{R: 200, G: 100, B: 50}}
	if scaleFrame(f, 1) != f {
		t.Fatal("уровень 1 должен возвращать кадр как есть")
	}
	if !scaleFrame(f, 0).IsZero() || !scaleFrame(f, -1).IsZero() {
		t.Fatal("уровень 0 — погашенный кадр")
	}
	if got := scaleFrame(f, 0.5)[3]; got.R < 98 || got.R > 102 || got.G < 48 || got.G > 52 {
		t.Fatalf("половина яркости: %+v", got)
	}
}

// У хвоста угасания нет долгого тусклого «теплится»: ярчайший канал дошёл до 2 из 255 — кадр гаснет целиком; а чуть ярче ещё горит.
func TestFadeTailEndsOnceTheBrightestChannelIsAlmostDark(t *testing.T) {
	f := ringled.Frame{3: led.Color{G: 60, B: 15}, 0: led.Color{B: 60}}
	for level, lit := range map[float64]bool{0.06: true, 0.05: true, 0.04: false, 0.01: false} {
		got := scaleFrame(f, level)
		if got.IsZero() == lit {
			t.Errorf("уровень %v: кадр %+v, горит ожидалось %v", level, got, lit)
		}
	}
	// полная яркость, настроенная очень тусклой, не обрезается: пользователь сам попросил так
	dim := ringled.Frame{3: led.Color{G: 2}}
	if scaleFrame(dim, 1) != dim {
		t.Fatal("тусклый кадр на полном уровне не должен гаснуть")
	}
}
