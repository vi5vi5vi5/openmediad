package spotifysoloist

import (
	"time"

	"openmediad/internal/core/quasar/buttons"
)

// Кнопка Алисы и проведение по верху колонки. Платформа превращает их в команды своему плееру: нажатие — «пауза/продолжить», проведение —
// запрос облаку «следующий/предыдущий трек», на который оно отвечает запуском очереди Яндекс.Музыки. Spotify у платформы своей аудиосессии
// нет, поэтому пауза уходит в пустоту, а от жеста вдруг играет Яндекс. Демон видит действие по логу платформы (core/quasar/buttons) раньше,
// чем облако ответит на жест, и отдаёт его сюда. Громкость и сами кнопки громкости Spotify не затрагивают: ими управляет Master.

// platformCmdEcho — сколько после команды, которую платформа сама адресовала плееру Spotify, нажатие из лога считается тем же самым:
// иначе кнопка сработала бы дважды и пауза тут же сменилась бы продолжением.
const platformCmdEcho = 1500 * time.Millisecond

// OnButton вызывает демон для каждого действия кнопок (интерфейс демона buttonHandler). true — действие выполнено в Spotify.
func (c *Component) OnButton(b buttons.Action) bool {
	cfg := c.config()
	if !cfg.Buttons || c.ctl == nil {
		return false
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn {
		return false
	}
	var a action
	switch b {
	case buttons.Next:
		a = actNext
	case buttons.Prev:
		a = actPrev
	case buttons.Toggle:
		c.mu.Lock()
		echo := time.Since(c.platformAt) < platformCmdEcho
		c.mu.Unlock()
		if echo {
			return false // платформа уже поставила на паузу или сняла сама, как со своим плеером
		}
		switch {
		case c.owns(actPause, ps):
			a = actPause
		case c.owns(actResume, ps):
			a = actResume
		default:
			return false // играет Яндекс или Spotify не хозяин звука: кнопка достаётся платформе
		}
	default:
		return false
	}
	if !c.owns(a, ps) {
		return false // жест во время музыки Яндекса листает Яндекс
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
		c.logf("колонка: %s не выполнена: %v", b, err)
		return false
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = b.String()
	c.voiceAt = time.Now()
	c.voiceAct = a // «дальше» от облака сразу после жеста — тот же жест
	c.mu.Unlock()
	if c.mach != nil && a != actPause {
		// платформа запросит у облака очередь Яндекса (жест, продолжить после паузы): этот запуск достанется Spotify
		c.mach.ArmCarrier(carrierWindow)
	}
	c.logf("колонка: %s — выполнено в Spotify", b)
	return true
}
