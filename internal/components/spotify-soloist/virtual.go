package spotifysoloist

import "openmediad/internal/core/quasar/mediad"

// Spotify как плеер платформы. Звук Spotify идёт через наш движок, но платформа о нём не знает: плееры музыки она заводит сама по
// директиве облака, а состояние плееров (играет, пауза, трек) берёт из событий mediad. Облако отвечает на «пауза», «играй», «дальше»
// без слова «Алиса» по последнему плееру платформы, а слой спектра на дисплее включается по состоянию плеера. Поэтому компонент
// сообщает автомату состояние Soloist (mediad.Machine.SetVirtual), автомат шлёт платформе те же события, что у своего плеера музыки,
// а команды платформы этому плееру (пауза, освобождение) приходят обратно демону, он отдаёт их ядру (core/control, источник Virtual), и ядро просит Spotify выполнить.

// startMusic выполняет «включи музыку»: продолжает прошлую сессию Spotify, а если продолжать нечего — запускает music_uri из настроек.
// false — вмешиваться нечем (отключено или не задан music_uri): фраза остаётся Алисе.
func (c *Component) startMusic(ps playerState) bool {
	if !c.config().Music {
		return false
	}
	return c.launch(ps)
}

// launch запускает Spotify: продолжает прошлую сессию, а если продолжать нечего, играет music_uri. false — запускать нечего или не вышло.
// Настройку music не смотрит: «включи спотифай» названо прямо и ей не подчиняется.
func (c *Component) launch(ps playerState) bool {
	cfg := c.config()
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
