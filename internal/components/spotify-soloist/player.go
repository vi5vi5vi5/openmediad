package spotifysoloist

import (
	"errors"
	"fmt"
	"time"

	playctl "openmediad/internal/core/control"
)

// Spotify как плеер ядра (core/control). Кнопка Алисы, жесты по колонке, кнопки наушников, голос и ответы облака не знают про Spotify: источник
// говорит ядру «пауза, откуда-то», а ядро решает, чей сейчас звук, не повтор ли это, и только потом просит Spotify выполнить. От нас нужно
// три вещи: ответить, каких источников мы слушаемся (Accepts), что с плеером сейчас (State) и выполнить команду (Do).
//
// Отдельный путь у нажатий на самой колонке: платформа адресует их своему плееру, а Spotify для неё не плеер. Пауза уходила в пустоту, а
// жест вперёд-назад она превращает в запрос облаку, и оно запускает очередь Яндекс.Музыки. Демон видит такие нажатия по логу платформы
// (core/quasar/buttons), а ядро готовит автомат к этому запуску (Origin.PlatformReacts).

// Accepts — слушается ли Spotify этого источника (настройки голоса, кнопок и плеера для платформы).
func (c *Component) Accepts(o playctl.Origin) bool {
	cfg := c.config()
	switch o {
	case playctl.Voice, playctl.FastVoice, playctl.Cloud:
		return cfg.Voice
	case playctl.Speaker, playctl.Headphones:
		return cfg.Buttons
	case playctl.Virtual:
		return cfg.VirtualPlayer
	case playctl.Arbiter:
		return true
	}
	return false
}

// State — что сейчас со Spotify.
func (c *Component) State() playctl.State {
	if c.ctl == nil {
		return playctl.State{}
	}
	ps := c.ctl.state()
	sounding := c.pcm != nil && c.pcm.Playing()
	// позиция приходит только с событием состояния, между ними она устаревает: играющей досчитываем по часам
	pos := ps.PositionMS
	if ps.Status == "playing" && !ps.PositionAt.IsZero() {
		pos += time.Since(ps.PositionAt).Milliseconds()
	}
	return playctl.State{
		Ready:    ps.Connected && ps.LoggedIn,
		Playing:  sounding || ps.Status == "playing",
		Sounding: sounding,
		// на паузе и выбран в приложении: Soloist помнит сессию, «продолжи» поднимет её
		Resumable: ps.Status == "paused" && ps.Active,
		Paused:    ps.Status == "paused",
		Track:     ps.URI,
		NearEnd:   ps.DurationMS > 0 && pos >= ps.DurationMS-nearEnd.Milliseconds(),
	}
}

// nearEnd — за сколько до конца трек считается доигрываемым: следующий после него начался сам, а не перемоткой.
const nearEnd = 4 * time.Second

// Do выполняет команду в Soloist. Команда уходит без ожидания результата: Soloist принимает команды по WebSocket.
func (c *Component) Do(cmd playctl.Cmd, from playctl.Origin) error {
	if c.ctl == nil {
		return errors.New("нет связи с Soloist")
	}
	var err error
	switch cmd {
	case playctl.Pause:
		err = c.ctl.Pause()
	case playctl.Resume:
		err = c.ctl.Resume()
	case playctl.Next:
		err = c.ctl.SkipNext()
	case playctl.Prev:
		err = c.ctl.SkipPrev()
	default:
		return fmt.Errorf("команда %q плееру не передаётся", cmd)
	}
	if err != nil {
		c.logf("%s (%s): не выполнено: %v", cmd, from, err)
		return err
	}
	if from == playctl.Arbiter {
		return nil // уступка звука — не команда пользователя
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = from.String() + ": " + cmd.String()
	c.voiceAt = time.Now()
	c.mu.Unlock()
	if from == playctl.Voice && c.config().VoiceCancel {
		go c.cancelDialog() // Алиса не должна отвечать «не поняла» на уже выполненную фразу. Ответ быстрой команды локальный и беззвучный, а его анимацию ждут
	}
	return nil
}

// Yielded — ядро уступало звук из-за Spotify или ему (для счётчика в статусе).
func (c *Component) Yielded() { c.noteYield() }
