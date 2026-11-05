package ledring

import (
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/libs/clockled"
)

// Анимации на дисплее с часами. Штатная музыка Яндекса сопровождается анимациями дисплея: пауза (две вертикали вокруг двоеточия), следующий трек
// (полосы от центра вправо), предыдущий (зеркально влево). Это файлы прошивки ledpatterns/clock/*.binscled.zst (формат разобран в libs/clockled).
// Когда паузу или перемотку делают кнопкой наушников или из приложения Spotify, прошивка о них не знает и дисплей молчит. Ядро (core/control)
// объявляет такие события, а компонент проигрывает на дисплее те же файлы, и выглядит это как у штатной музыки.

// clockAnimNames — какой файл прошивки играет на какое событие. У «продолжить» анимации дисплея в прошивке нет.
var clockAnimNames = map[control.Cmd]string{
	control.Pause: clockled.AnimPause,
	control.Next:  clockled.AnimNext,
	control.Prev:  clockled.AnimPrevious,
}

// platformDrawsDisplay — прошивка рисует эту анимацию на дисплее сама. Ответ на короткую команду и директива облака приходят с анимацией
// (draw_animation_directive), команду усыновлённому плееру платформа выполняет как команду своего. Пауза кнопкой колонки тоже локальна, и анимация
// приходит сразу. А пауза фразой с «Алисой» — нет: диалог мы отменяем, и ответа с анимацией не будет (по журналу платформы там только «Алиса слушает»
// и «думает»). Перемотку кнопкой колонки и фразой платформа отправляет облаку, и в ответе (audio_play) анимации нет: её рисуем мы.
func platformDrawsDisplay(cmd control.Cmd, from control.Origin) bool {
	switch from {
	case control.FastVoice, control.Cloud, control.Virtual:
		return true
	case control.Speaker:
		return cmd == control.Pause
	}
	return false
}

// maxDisplayWait — сколько ждём, пока прошивка освободит дисплей (спектр музыки, «Алиса думает»), прежде чем отказаться от анимации: не вовремя она не нужна.
var maxDisplayWait = 3 * time.Second

// spectrumHoldService — общая служба display-spectrum: убрать спектр музыки с дисплея на время анимации. Платформа рисует спектр поверх всего ещё 15 секунд
// после начала каждого трека и перебивает анимации паузы и перемотки. Служба необязательна: нет компонента — ждём, пока дисплей освободится сам.
const spectrumHoldService = "spectrum-hold"

func (c *Component) config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// loadClockAnims читает штатные анимации дисплея из каталога прошивки. Нет файла — нет и анимации на это событие.
func (c *Component) loadClockAnims() {
	dir := c.clockDir
	if dir == "" {
		dir = clockled.AnimDir
	}
	got := map[control.Cmd]*clockled.Anim{}
	var lastErr error
	for cmd, name := range clockAnimNames {
		a, err := clockled.LoadAnim(dir, name)
		if err != nil {
			lastErr = err
			continue
		}
		got[cmd] = a
	}
	if len(got) < len(clockAnimNames) {
		c.logf("анимации дисплея доступны не все (%d из %d): %v", len(got), len(clockAnimNames), lastErr)
	}
	c.mu.Lock()
	c.clockAnims = got
	c.mu.Unlock()
}

// displayEvent решает, играть ли анимацию на дисплее, и запускает её, когда дисплей освободится.
func (c *Component) displayEvent(e control.Event, what string) {
	c.mu.Lock()
	a, pl, scale := c.clockAnims[e.Cmd], c.player, uint8(c.cfg.AnimationBrightness)
	switch {
	case pl == nil:
		c.mu.Unlock()
		return
	case platformDrawsDisplay(e.Cmd, e.From):
		c.note("%s: дисплей: пропущено, анимацию рисует прошивка", what)
		c.mu.Unlock()
		return
	case a == nil:
		c.note("%s: дисплей: у прошивки нет анимации", what)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	go c.playDisplay(a, scale, what)
}

// playDisplay убирает спектр музыки, ждёт, пока дисплей свободен (прошивка показывает время), запускает анимацию и возвращает спектр, когда она доиграна.
func (c *Component) playDisplay(a *clockled.Anim, scale uint8, what string) {
	start := time.Now()
	if c.holdSpectrum != nil {
		defer c.holdSpectrum()()
	}
	for {
		if c.curAnim == nil || c.curAnim().ClockIdle() {
			c.player.Play(a, scale, what)
			c.mu.Lock()
			c.note("%s: дисплей: играем через %d мс", what, time.Since(start).Milliseconds())
			c.mu.Unlock()
			c.waitPlayed(a)
			return
		}
		if time.Since(start) >= maxDisplayWait {
			c.mu.Lock()
			c.note("%s: дисплей так и не освободился за %v, анимация отброшена", what, maxDisplayWait)
			c.mu.Unlock()
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// waitPlayed ждёт, пока доиграет анимация (и те, что заменили её): до этого спектр музыки обратно на дисплей не вернуть.
func (c *Component) waitPlayed(a *clockled.Anim) {
	for end := time.Now().Add(a.Lit() + 2*time.Second); time.Now().Before(end) && c.player.Active(); {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}
