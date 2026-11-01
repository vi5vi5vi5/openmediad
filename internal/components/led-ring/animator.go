package ledring

import (
	"time"

	"openmediad/internal/libs/ringled"
)

// Подсветка не горит всё время, а вспыхивает: когда началась музыка (или сменился источник или выход), кольцо загорается на hold секунд вместе со
// спектрограммой на часах и плавно гаснет за fade. Отсчёт hold начинается, когда кадр действительно появился на кольце (прошивка в начале трека ещё
// рисует свои анимации).

type phase int

const (
	phaseIdle    phase = iota // не горит
	phaseHolding              // горит на полную
	phaseFading               // плавно гаснет
)

// maxLightWait — сколько ждём, пока кадр появится на кольце, прежде чем начать отсчёт удержания всё равно (прошивка могла надолго занять кольцо).
const maxLightWait = 4 * time.Second

// animator — время жизни вспышки: ничего не знает ни про железо, ни про цвета, только про уровень яркости 0…1.
type animator struct {
	phase   phase
	since   time.Time // начало фазы
	from    float64   // уровень, с которого началось угасание
	waiting bool      // зажечь просили, кадр ещё не на кольце
	asked   time.Time
}

func (a *animator) Active() bool { return a.phase != phaseIdle }

// Trigger — появилось, что показать: (заново) зажечь на полную.
func (a *animator) Trigger(now time.Time) {
	a.phase, a.waiting, a.asked = phaseHolding, true, now
}

// Stop — показывать больше нечего: плавно погаснуть с того уровня, на котором сейчас горит. lit — кадр уже на кольце (вспышка могла успеть
// загореться раньше, чем мы это заметили).
func (a *animator) Stop(now time.Time, lit bool) {
	if a.phase != phaseHolding {
		return // не горело или уже гаснет
	}
	if a.waiting && !lit { // не успело даже загореться
		a.phase = phaseIdle
		return
	}
	a.phase, a.since, a.from = phaseFading, now, 1
}

// Level — яркость на момент now (0…1). lit — наш кадр сейчас на кольце (прошивка его не перебивает). hold == 0 — держать, пока есть что показывать.
func (a *animator) Level(now time.Time, hold, fade time.Duration, lit bool) float64 {
	switch a.phase {
	case phaseHolding:
		if a.waiting {
			if lit || now.Sub(a.asked) >= maxLightWait {
				a.waiting, a.since = false, now
			}
			return 1
		}
		if hold > 0 && now.Sub(a.since) >= hold {
			a.phase, a.since, a.from = phaseFading, a.since.Add(hold), 1 // угасание начинается ровно в конце удержания, а не когда мы заметили
			return a.Level(now, hold, fade, lit)
		}
		return 1
	case phaseFading:
		if fade <= 0 || now.Sub(a.since) >= fade {
			a.phase = phaseIdle
			return 0
		}
		p := float64(now.Sub(a.since)) / float64(fade)
		return a.from * (1 - p) * (1 - p) // глаз видит яркость примерно в квадрате: так угасание выглядит ровным
	}
	return 0
}

// dimFloor — на этом значении канала (из 255) и ниже светодиод у затухающего кадра считается погасшим: у светодиодов яркость нелинейна, и
// 1–2 из 255 глаз видит как «ещё теплится», а длинный хвост такого свечения превращает конец угасания в долгое «не до конца».
const dimFloor = 2

// scaleFrame гасит кадр до уровня level (0…1). Когда ярчайший канал опускается до dimFloor, кадр гаснет целиком.
func scaleFrame(f ringled.Frame, level float64) ringled.Frame {
	if level >= 1 {
		return f
	}
	if level <= 0 {
		return ringled.Frame{}
	}
	b := uint8(level*255 + 0.5)
	var peak uint8
	for i := range f {
		f[i] = f[i].Scale(b)
		peak = max(peak, f[i].R, f[i].G, f[i].B)
	}
	if peak <= dimFloor {
		return ringled.Frame{}
	}
	return f
}
