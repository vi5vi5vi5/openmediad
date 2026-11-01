package spotifysoloist

import (
	"time"

	playctl "openmediad/internal/core/control"
)

// Spotify как плеер ядра (core/control). Кнопка Алисы, жесты по колонке и кнопки наушников не знают про Spotify: источник говорит ядру
// «пауза, откуда-то», а ядро спрашивает плееры, чей сейчас звук. Спросит и нас; мы отвечаем: «моё, сделал» или «не моё».
//
// Платформа же про Spotify ничего не знает, и это важно для нажатий на самой колонке: она адресует их своему плееру. Пауза уходит в пустоту,
// а жест вперёд-назад она превращает в запрос облаку, и оно запускает очередь Яндекс.Музыки. Демон видит такие нажатия по логу платформы
// (core/quasar/buttons) раньше, чем придёт ответ облака, и отдаёт их ядру с пометкой Speaker. Кнопки наушников читает только движок, платформа о них
// не знает и ничего не затеет (Origin.PlatformReacts), поэтому очередь Яндекса для них готовить не нужно.

// platformCmdEcho — сколько после команды, которую платформа сама адресовала плееру Spotify, нажатие кнопки колонки из лога считается тем же самым:
// иначе кнопка сработала бы дважды и пауза тут же сменилась бы продолжением.
const platformCmdEcho = 1500 * time.Millisecond

// Handle — интерфейс control.Player: выполнить команду, если звук сейчас Spotify. false — не наше (играет Яндекс, нет входа, выключено).
func (c *Component) Handle(cmd playctl.Cmd, from playctl.Origin) bool {
	cfg := c.config()
	if !cfg.Buttons || c.ctl == nil {
		return false
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn {
		return false
	}
	var a action
	switch cmd {
	case playctl.Next:
		a = actNext
	case playctl.Prev:
		a = actPrev
	case playctl.Pause:
		a = actPause
	case playctl.Resume:
		a = actResume
	case playctl.Toggle:
		if from == playctl.Speaker {
			c.mu.Lock()
			echo := time.Since(c.platformAt) < platformCmdEcho
			c.mu.Unlock()
			if echo {
				return false // платформа уже поставила на паузу или сняла сама, как со своим плеером
			}
		}
		switch {
		case c.owns(actPause, ps):
			a = actPause
		case c.owns(actResume, ps):
			a = actResume
		default:
			return false // играет Яндекс или Spotify не хозяин звука: команда достаётся платформе
		}
	default:
		return false
	}
	if !c.owns(a, ps) {
		return false // во время музыки Яндекса команды его
	}
	var err error
	switch a {
	case actPause:
		err = c.ctl.Pause()
	case actResume:
		err = c.ctl.Resume()
	case actNext:
		err = c.ctl.SkipNext()
	case actPrev:
		err = c.ctl.SkipPrev()
	}
	if err != nil {
		c.logf("%s (%s): не выполнено: %v", cmd, from, err)
		return false
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = from.String() + ": " + cmd.String()
	c.voiceAt = time.Now()
	c.voiceAct = a // «дальше» от облака сразу после жеста — тот же жест
	c.mu.Unlock()
	if c.mach != nil && a != actPause && from.PlatformReacts() {
		// платформа запросит у облака очередь Яндекса (жест, продолжить после паузы): этот запуск достанется Spotify
		c.mach.ArmCarrier(carrierWindow)
	}
	return true
}
