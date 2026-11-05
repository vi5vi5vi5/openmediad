package spotifysoloist

import (
	"context"
	"time"

	alicedclient "openmediad/internal/libs/aliced-client"
)

// Голос и Spotify. «Алиса, пауза / дальше / назад / продолжи» и короткие команды без «Алиса» ведёт ядро (core/control): оно знает, чей сейчас
// звук, и отдаёт команду плееру (player.go). Здесь остаётся то, что относится только к Spotify: лайк, «включи музыку» и «включи <название>».
// Приложение Spotify на колонке Алису не слышит: фраза уходит в облако Яндекса, а оно про Spotify не знает и отвечает «не поняла». Демон отдаёт
// компоненту окончательную фразу (InterceptPhrase) раньше ответа облака; если фраза из грамматики (phrases.go) и Spotify сейчас хозяин звука,
// компонент выполняет её сам, отменяет диалог Алисы и просит демон погасить ответ облака, если он всё-таки придёт. Во всех остальных случаях
// фраза идёт Алисе как обычно.

// carrierWindow — сколько после выполненной фразы запуск музыки Яндекса платформой считается её следствием: audio_play приходит
// через 0,1–0,7 с после фразы.
const carrierWindow = 3 * time.Second

// cancelTimeout — сколько ждём отправки отмены диалога: после окончательной фразы до ответа облака около секунды.
const cancelTimeout = 2 * time.Second

// owns говорит, относится ли действие, не связанное с воспроизведением, к Spotify прямо сейчас. Чей звук, решает ядро.
func (c *Component) owns(a action) bool {
	if c.hub == nil {
		return false
	}
	switch a {
	case actLike, actUnlike:
		return c.hub.Owns(Name)
	case actPlayMusic:
		return c.hub.Idle() // ничего не играет: «включи музыку» — просьба заиграть что-то
	case actStartService:
		return true // Spotify назван прямо: что бы ни играло, просят именно его
	}
	return false
}

// book — слова и фразы голосовых просьб (файл phrases.txt, а пока его нет — встроенные).
func (c *Component) book() *phraseBook { return c.phrases.get() }

// hintNothingToStart пишет в журнал, почему «включи спотифай» запускать нечем (не чаще раза в 10 минут).
func (c *Component) hintNothingToStart() {
	c.mu.Lock()
	show := time.Since(c.startHintAt) > searchHintEvery
	if show {
		c.startHintAt = time.Now()
	}
	c.mu.Unlock()
	if show {
		c.logf("«включи спотифай»: запускать нечего — Soloist не помнит прошлую сессию, а music_uri не задан. Выберите «Мини 3» в приложении Spotify и включите что-нибудь один раз, " +
			"либо задайте: openmediad config set spotify-soloist.music_uri spotify:playlist:… (фраза пока уходит Алисе)")
	}
}

// InterceptPhrase вызывает демон для каждой окончательной фразы, которую не взял плеер ядра (интерфейс демона phraseInterceptor). true — фраза
// выполнена здесь, ответ облака голосом гасится. Не блокирует: Soloist принимает команды по WebSocket без ожидания результата.
func (c *Component) InterceptPhrase(reqID, text string, fast bool) bool {
	cfg := c.config()
	if !cfg.Voice || c.ctl == nil {
		return false
	}
	a := parsePhrase(text)
	if a == actNone {
		if fast {
			return false // быстрые команды платформы — это «пауза», «дальше», а не просьбы с названием
		}
		r, ok := c.book().parsePlayRequest(text)
		switch {
		case !ok:
			return false
		case r.Start:
			a = actStartService // «включи спотифай», «включи музыку в спотике»: Spotify назван прямо, искать нечего
		default:
			if cfg.PlayByName {
				// «включи Кино», «включи песню …»: ищем и решаем позже (request.go); саму фразу не забираем
				c.beginPlayRequest(reqID, r)
			}
			return false
		}
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn || !c.owns(a) {
		return false
	}
	switch a {
	case actPlayMusic:
		if !c.startMusic(ps) {
			return false
		}
	case actStartService:
		if !c.launch(ps) {
			c.hintNothingToStart()
			return false
		}
	case actLike, actUnlike:
		if !c.startLike(ps, a == actLike) {
			return false // нет входа в Web API или играет не трек: фраза остаётся Алисе
		}
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = a.String()
	c.voiceAt = time.Now()
	c.voiceAct = a
	c.mu.Unlock()
	how := ""
	if fast {
		how = ", быстрая команда"
	}
	c.logf("голос: %s — выполнено вместо Алисы (запрос %.8s%s)", a, reqID, how) // сам текст фразы в журнал не пишем: фразы приватны
	if c.mach != nil && (a == actPlayMusic || a == actStartService) {
		// платформа сама запустит очередь Яндекса (audio_play после «включи музыку»): этот запуск достанется Spotify. Названный сервис облако
		// сначала ищет как музыку и отвечает не сразу, поэтому окно длиннее
		w := carrierWindow
		if a == actStartService {
			w = playArmWindow
		}
		c.mach.ArmCarrier(w)
	}
	if cfg.VoiceCancel && !fast { // ответ быстрой команды локальный и беззвучный, а его анимацию пользователь ждёт
		go c.cancelDialog()
	}
	return true
}

// directiveEcho — сколько после перехваченной фразы директива того же действия считается повтором: фразу уже выполнили, а облако,
// если отмена диалога не успела, всё равно ответило.
const directiveEcho = 6 * time.Second

// OnDirective получает имя каждой директивы ответа Алисы (интерфейс демона directiveHandler). Директивы управления плеером (player_pause …)
// разбирает ядро; здесь только player_like: облако поняло «лайк» без слова «Алиса», а лайк ставит Spotify.
func (c *Component) OnDirective(name string) {
	if name != "player_like" {
		return
	}
	cfg := c.config()
	if !cfg.Voice || c.ctl == nil {
		return
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn || !c.owns(actLike) {
		return
	}
	c.mu.Lock()
	echo := c.voiceAct == actLike && time.Since(c.voiceAt) < directiveEcho
	c.mu.Unlock()
	if echo {
		return
	}
	if !c.startLike(ps, true) {
		return
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = actLike.String() + " (директива облака)"
	c.voiceAt = time.Now()
	c.voiceAct = actLike
	c.mu.Unlock()
	c.logf("голос: директива облака %s — выполнено в Spotify", name)
}

// cancelDialog отменяет диалог Алисы, чтобы облако не отвечало на уже выполненную фразу.
func (c *Component) cancelDialog() {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	if err := c.cancelFn(ctx); err != nil {
		c.logf("голос: диалог Алисы не отменён: %v (ответ облака может прозвучать)", err)
	}
}

// voiceStatus — статистика голосового управления (для status -v).
type voiceStatus struct {
	Enabled bool      `json:"enabled"`
	Cancel  bool      `json:"cancel_dialog"`
	Handled int       `json:"handled"`
	Likes   int       `json:"likes"`                // лайков поставлено или снято
	LikeOK  bool      `json:"like_ready"`           // для лайков есть client_id и вход
	LikeErr string    `json:"like_error,omitempty"` // чем кончился последний лайк, если неудачей
	Last    string    `json:"last,omitempty"`
	LastAt  time.Time `json:"last_at,omitzero"`
	Owner   string    `json:"owner,omitempty"` // кто последним начал играть
}

func (c *Component) voiceStatus() voiceStatus {
	cfg := c.config()
	owner := ""
	if c.hub != nil {
		owner = c.hub.Owner()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ready := c.web != nil && cfg.Like && c.web.ready() == nil
	return voiceStatus{Enabled: cfg.Voice, Cancel: cfg.VoiceCancel, Likes: c.likes, LikeOK: ready, LikeErr: c.likeErr, Handled: c.voiceHandled, Last: c.voiceLast, LastAt: c.voiceAt, Owner: owner}
}

// defaultCancel — отмена диалога через aliced (порт 9875), как кнопка 'c' в консоли maind.
func defaultCancel(ctx context.Context) error {
	return (&alicedclient.Client{}).Cancel(ctx)
}
