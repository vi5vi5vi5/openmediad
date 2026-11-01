package ringled

import (
	"context"
	"sync"
	"time"
)

// Keeper держит на кольце нужный кадр. Нужный кадр задаёт Set; нулевой кадр означает «кольцо не нужно» — оно отдаётся прошивке.
type Keeper struct {
	Dev  Device
	Anim func() Anim // последнее известное переключение анимаций прошивки
	Logf func(string, ...any)

	Poll   time.Duration // как часто сверять кольцо с записанным, пока оно нужно (по умолчанию 20 мс)
	Idle   time.Duration // как часто просыпаться, когда кольцо не нужно (по умолчанию 250 мс); Set будит сразу
	Settle time.Duration // сколько кадр не должен меняться после анимации прошивки, прежде чем рисовать своё (по умолчанию 300 мс: затухание)
	// FastSettle — то же, когда прошивка просто погасила кольцо (idle.led, без анимации). Гасит она мгновенно, затухания ждать незачем;
	// иначе кольцо оставалось бы тёмным по 300 мс (по умолчанию 20 мс).
	FastSettle time.Duration
	Now        func() time.Time // часы (по умолчанию time.Now)

	mu      sync.Mutex
	want    Frame
	owned   bool  // кольцо нужно нам
	active  bool  // наш кадр сейчас горит
	written Frame // что мы записали последним
	actual  Frame // что в кольце по последней проверке
	changed time.Time
	yields  int // сколько раз уступали прошивке
	wake    chan struct{}

	verifyAt    time.Time // когда проверить, что после отдачи кольцо действительно погасло
	verifyTries int
}

// clearCheck — через сколько после отдачи кольца проверить, что наш кадр с него ушёл, и сколько раз гасить повторно.
const (
	clearCheck = 150 * time.Millisecond
	clearTries = 3
)

// wakeLocked — канал, которым Set будит Run (создаётся при первом обращении; mu занят).
func (k *Keeper) wakeLocked() chan struct{} {
	if k.wake == nil {
		k.wake = make(chan struct{}, 1)
	}
	return k.wake
}

// pause — через сколько Run проснётся в следующий раз.
func (k *Keeper) pause() (time.Duration, <-chan struct{}) {
	k.mu.Lock()
	defer k.mu.Unlock()
	d, def := k.Idle, 250*time.Millisecond
	if k.owned {
		d, def = k.Poll, 20*time.Millisecond
	}
	if d <= 0 {
		d = def
	}
	if !k.verifyAt.IsZero() {
		d = min(d, 50*time.Millisecond) // кольцо только что отдали: скоро проверять, что оно погасло
	}
	return d, k.wakeLocked()
}

func (k *Keeper) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Keeper) logf(f string, a ...any) {
	if k.Logf != nil {
		k.Logf(f, a...)
	}
}

func (k *Keeper) anim() Anim {
	if k.Anim == nil {
		return Anim{}
	}
	return k.Anim()
}

// Set задаёт нужный кадр. Кадр, совпадающий с уже заданным, ничего не делает.
func (k *Keeper) Set(f Frame) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if f.IsZero() {
		k.release()
		return
	}
	if k.owned && k.want == f {
		return
	}
	k.want = f
	if !k.owned {
		k.owned = true
		k.logf("кольцо: берём под управление")
		select {
		case k.wakeLocked() <- struct{}{}:
		default:
		}
	}
	k.apply(k.now())
}

// Release отдаёт кольцо прошивке.
func (k *Keeper) Release() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.release()
}

func (k *Keeper) release() {
	if !k.owned {
		return
	}
	k.owned = false
	k.want = Frame{}
	if k.active {
		// В покое прошивка держит кольцо погашенным (idle.led); если она сейчас что-то рисует, наш кадр уже стёрт и active был бы false.
		if err := k.Dev.Write(Frame{}); err != nil {
			k.logf("кольцо: запись: %v", err)
		}
		k.actual, k.active = Frame{}, false
	}
	k.logf("кольцо: отдаём прошивке")
	// Погасло ли на самом деле, проверим чуть позже: потерянная запись оставила бы на кольце последний тусклый кадр.
	k.verifyAt, k.verifyTries = k.now().Add(clearCheck), 0
	select {
	case k.wakeLocked() <- struct{}{}:
	default:
	}
}

// verifyClear после отдачи кольца убеждается, что наш кадр с него ушёл. Остался ровно он — гасит снова; что-то другое (прошивка рисует
// своё) не трогает.
func (k *Keeper) verifyClear(now time.Time) {
	if k.verifyAt.IsZero() || now.Before(k.verifyAt) {
		return
	}
	k.verifyAt = time.Time{}
	if k.owned {
		return // кольцо снова нужно: дальше его ведёт обычная сверка
	}
	cur, err := k.Dev.Read()
	if err != nil || cur.IsZero() || cur != k.written {
		return
	}
	k.logf("кольцо: после отдачи на нём остался наш кадр — гашу снова")
	if err := k.Dev.Write(Frame{}); err != nil {
		k.logf("кольцо: запись: %v", err)
		return
	}
	k.actual = Frame{}
	if k.verifyTries++; k.verifyTries < clearTries {
		k.verifyAt = now.Add(clearCheck)
	}
}

// Tick — один шаг сверки (раз в Poll): заметить, что прошивка нарисовала поверх нас, и вернуть своё, когда она закончила.
func (k *Keeper) Tick(now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.verifyClear(now)
	if !k.owned {
		return // кольцо не нужно: не читаем его зря
	}
	actual, err := k.Dev.Read()
	if err != nil {
		k.logf("кольцо: чтение: %v", err)
		return
	}
	if actual != k.actual {
		k.actual = actual
		if !k.active || actual != k.written {
			k.changed = now
		}
	}
	if k.active && actual != k.written {
		k.active = false // прошивка нарисовала поверх нас — пусть доиграет
		k.yields++
	}
	if !k.active {
		k.apply(now)
	}
}

// apply рисует наш кадр, если он уже горит (обновить) или прошивка освободила кольцо.
func (k *Keeper) apply(now time.Time) {
	settle := k.Settle
	if settle <= 0 {
		settle = 300 * time.Millisecond
	}
	if k.actual.IsZero() {
		if settle = k.FastSettle; settle <= 0 {
			settle = 20 * time.Millisecond
		}
	}
	if !k.active {
		a := k.anim()
		if !(a.RingIdle() && now.Sub(k.changed) >= settle && now.Sub(a.At) >= settle) {
			return
		}
	}
	if err := k.Dev.Write(k.want); err != nil {
		k.logf("кольцо: запись: %v", err)
		return
	}
	k.written, k.actual, k.active = k.want, k.want, true
}

// Run сверяет кольцо, пока не отменят ctx; на выходе отдаёт кольцо прошивке.
func (k *Keeper) Run(ctx context.Context) {
	for {
		d, wake := k.pause()
		select {
		case <-ctx.Done():
			k.Release()
			return
		case <-wake: // кольцо понадобилось: дальше сверяем часто
		case <-time.After(d):
			k.Tick(k.now())
		}
	}
}

// Status — состояние для статуса.
type Status struct {
	Owned  bool
	Active bool // наш кадр горит; false при owned — уступили прошивке
	Want   Frame
	Yields int
}

func (k *Keeper) Status() Status {
	k.mu.Lock()
	defer k.mu.Unlock()
	return Status{Owned: k.owned, Active: k.active, Want: k.want, Yields: k.yields}
}
