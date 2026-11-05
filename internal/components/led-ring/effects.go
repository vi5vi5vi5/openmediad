package ledring

import (
	"fmt"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/libs/ringled"
)

// Анимации плеера. Штатная музыка Яндекса сопровождается анимациями прошивки: пауза, продолжение, следующий и предыдущий трек (файлы
// player_*.led). Когда паузу ставят из приложения Spotify или кнопкой в наушниках, прошивка о ней не знает, и кольцо молчит. Ядро
// (core/control) объявляет такие события независимо от того, откуда они пришли, а компонент проигрывает на кольце те же файлы прошивки, и
// выглядит это так же, как со штатной музыкой.

// effectNames — какой файл прошивки играет на какое событие.
var effectNames = map[control.Cmd]string{
	control.Pause:  ringled.PatternPause,
	control.Resume: ringled.PatternContinue,
	control.Next:   ringled.PatternNext,
	control.Prev:   ringled.PatternPrevious,
}

// platformDraws — источники, при которых прошивка рисует свою анимацию сама: короткая команда платформы отвечает локальным сценарием с анимацией,
// ответ облака приносит её директивой, а команду усыновлённому плееру платформа выполняет как команду своего. Второй раз поверх рисовать незачем.
func platformDraws(o control.Origin) bool {
	return o == control.FastVoice || o == control.Cloud || o == control.Virtual
}

// maxEffectWait — сколько ждём кольцо у прошивки (после фразы или жеста Алиса слушает, думает, отвечает), прежде чем отказаться от анимации:
// показывать её уже не вовремя.
var maxEffectWait = 5 * time.Second

// effect — проигрываемая сейчас анимация.
type effect struct {
	p       *ringled.Pattern
	asked   time.Time
	start   time.Time
	waiting bool          // кольцо ещё у прошивки: отсчёт начнётся, когда первый кадр анимации появится на нём
	first   ringled.Frame // первый кадр анимации, каким он должен лечь на кольцо
	what    string        // что за анимация (для журнала разбора)
}

// note запоминает решение по анимации для разбора (status -v): последние несколько штук.
func (c *Component) note(f string, a ...any) {
	line := time.Now().Format("15:04:05.000") + " " + fmt.Sprintf(f, a...)
	c.fxLog = append(c.fxLog, line)
	if len(c.fxLog) > 40 {
		c.fxLog = c.fxLog[len(c.fxLog)-40:]
	}
}

// loadPatterns читает штатные анимации плеера из каталога прошивки. Нет файла — нет и анимации на это событие.
func (c *Component) loadPatterns() {
	dir := c.patternDir
	if dir == "" {
		dir = ringled.PatternDir
	}
	got := map[control.Cmd]*ringled.Pattern{}
	var lastErr error
	for cmd, name := range effectNames {
		p, err := ringled.LoadPattern(dir, name)
		if err != nil {
			lastErr = err
			continue
		}
		got[cmd] = p
	}
	if len(got) < len(effectNames) {
		c.logf("анимации плеера доступны не все (%d из %d): %v", len(got), len(effectNames), lastErr)
	}
	c.mu.Lock()
	c.patterns = got
	c.mu.Unlock()
}

// onEvent — ядро объявило событие плеера: пауза, продолжение, перемотка. Дисплей и кольцо играют независимо (настройки animations и ring_animations).
func (c *Component) onEvent(e control.Event) {
	cfg := c.config()
	what := fmt.Sprintf("%s (%s)", e.Cmd, e.From)
	if cfg.Animations {
		c.displayEvent(e, what)
	}
	if cfg.RingAnimations {
		c.ringEvent(e, what)
	}
}

// ringEvent — анимация кольца логотипа (файл player_*.led).
func (c *Component) ringEvent(e control.Event, what string) {
	if c.keeper == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	if platformDraws(e.From) {
		c.note("%s: кольцо: пропущено, анимацию рисует прошивка", what)
		c.mu.Unlock()
		return
	}
	p := c.patterns[e.Cmd]
	if p == nil {
		c.note("%s: кольцо: нет файла анимации", what)
		c.mu.Unlock()
		return
	}
	first, _ := p.At(0)
	c.fx = &effect{p: p, asked: now, waiting: true, first: first.Scale(uint8(c.cfg.AnimationBrightness)), what: what}
	c.note("%s: кольцо: ждём", what)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	c.step(now)
}

// effectFrame — кадр анимации на момент now (c.mu занят). false — анимации нет или она кончилась. Отсчёт начинается, когда наш кадр действительно
// появился на кольце: пока там анимация прошивки (Алиса думает после жеста), мы ждём, но не дольше maxEffectWait.
func (c *Component) effectFrame(now time.Time) (ringled.Frame, bool) {
	fx := c.fx
	if fx == nil {
		return ringled.Frame{}, false
	}
	if fx.waiting {
		// Отсчёт начинается, когда первый кадр действительно лёг на кольцо: пока там анимация прошивки (слушает, думает, отвечает), мы ждём
		// и держим его наготове. Иначе анимация проигралась бы под чужой.
		if c.keeper != nil {
			if st := c.keeper.Status(); st.Active && st.Written == fx.first {
				fx.waiting, fx.start = false, now
				c.note("%s: кольцо у нас через %d мс, играем", fx.what, now.Sub(fx.asked).Milliseconds())
			}
		}
		if fx.waiting {
			if now.Sub(fx.asked) >= maxEffectWait {
				c.note("%s: кольцо так и не освободилось за %v, анимация отброшена", fx.what, maxEffectWait)
				c.fx = nil
				return ringled.Frame{}, false
			}
			return fx.first, true
		}
	}
	el := now.Sub(fx.start)
	if el >= fx.p.Lit() {
		c.fx = nil
		return ringled.Frame{}, false
	}
	f, _ := fx.p.At(el)
	return f.Scale(uint8(c.cfg.AnimationBrightness)), true
}

// effectActive — анимация играет (c.mu не нужен).
func (c *Component) effectActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fx != nil
}
