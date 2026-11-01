package spotifysoloist

import "openmediad/internal/core/quasar/mediad"

// Spotify как плеер платформы. Звук Spotify идёт через наш движок, но платформа о нём не знает: плееры музыки она заводит сама по
// директиве облака, а состояние плееров (играет, пауза, трек) берёт из событий mediad. Облако отвечает на «пауза», «играй», «дальше»
// без слова «Алиса» по последнему плееру платформы, а слой спектра на дисплее включается по состоянию плеера. Поэтому компонент
// сообщает автомату состояние Soloist (mediad.Machine.SetVirtual), автомат шлёт платформе те же события, что у своего плеера музыки,
// а команды платформы этому плееру (пауза, освобождение) приходят обратно в onVirtualCommand и идут в Soloist.

// startMusic выполняет «включи музыку»: продолжает прошлую сессию Spotify, а если продолжать нечего — запускает music_uri из настроек.
// false — вмешиваться нечем (отключено или не задан music_uri): фраза остаётся Алисе.
func (c *Component) startMusic(ps playerState) bool {
	cfg := c.config()
	if !cfg.Music {
		return false
	}
	switch {
	case ps.URI != "": // Soloist помнит последний трек и после перезапуска, даже если устройство не выбрано в приложении
		if err := c.ctl.Resume(); err != nil {
			c.logf("голос: включить музыку не вышло: %v", err)
			return false
		}
	case cfg.MusicURI != "":
		if err := c.ctl.PlayURI(cfg.MusicURI); err != nil {
			c.logf("голос: включить музыку не вышло: %v", err)
			return false
		}
	default:
		return false
	}
	return true
}

// virtualPush сообщает автомату, что сейчас делает Spotify. Вызывается при каждом изменении состояния Soloist; повторы автомат отсекает сам.
func (c *Component) virtualPush() {
	if c.mach == nil || c.ctl == nil {
		return
	}
	ps := c.ctl.state()
	if !c.config().VirtualPlayer || !ps.Connected || !ps.LoggedIn || ps.URI == "" {
		c.mach.SetVirtual(nil)
		return
	}
	switch ps.Status {
	case "playing", "paused":
	case "buffering", "loading":
		return // промежуточное состояние: платформе ничего не сообщаем
	default:
		c.mach.SetVirtual(nil)
		return
	}
	c.mach.SetVirtual(&mediad.Virtual{
		Track: ps.URI, Title: ps.Title, Artist: ps.Artist,
		DurMs: uint64(max(ps.DurationMS, 0)), PosMs: uint64(max(ps.PositionMS, 0)), PosAt: ps.PositionAt,
		Playing: ps.Status == "playing",
	})
}

// onVirtualCommand — платформа адресовала команду плееру Spotify: пауза или освобождение (кнопка, «Алиса, пауза» с плеером в контексте)
// ставят Spotify на паузу, play — продолжают.
func (c *Component) onVirtualCommand(cmd string) {
	if c.ctl == nil || !c.config().VirtualPlayer {
		return
	}
	var err error
	switch cmd {
	case "pause", "release":
		err = c.ctl.Pause()
	case "play":
		err = c.ctl.Resume()
	default:
		c.logf("платформа → Spotify: непонятная команда %q плееру", cmd)
		return
	}
	if err != nil {
		c.logf("платформа → Spotify: %s не выполнена: %v", cmd, err)
		return
	}
	c.logf("платформа → Spotify: %s (как плееру музыки)", cmd)
}
