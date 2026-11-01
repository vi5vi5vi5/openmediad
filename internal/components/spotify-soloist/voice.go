package spotifysoloist

import (
	"context"
	"time"

	alicedclient "openmediad/internal/libs/aliced-client"
)

// Голосовое управление Spotify. Приложение Spotify на колонке не слышит Алису: «Алиса, пауза» уходит в облако Яндекса, а оно не знает про
// Spotify и отвечает «не поняла». Демон отдаёт компоненту окончательную фразу (InterceptPhrase) раньше ответа облака; если фраза
// из грамматики (phrases.go) и Spotify сейчас хозяин звука, компонент выполняет её сам, отменяет диалог Алисы и просит демон
// погасить ответ облака, если он всё-таки придёт. Во всех остальных случаях фраза идёт Алисе как обычно.

// Кто последним начал играть: от этого зависит, чьё «продолжи» после паузы.
const (
	ownerSpotify = "spotify"
	ownerYandex  = "yandex"
)

// carrierWindow — сколько после выполненной фразы запуск музыки Яндекса платформой считается её следствием: audio_play приходит
// через 0,1–0,7 с после фразы.
const carrierWindow = 3 * time.Second

// cancelTimeout — сколько ждём отправки отмены диалога: после окончательной фразы до ответа облака около секунды.
const cancelTimeout = 2 * time.Second

func (c *Component) setOwner(o string) {
	c.mu.Lock()
	c.owner = o
	c.mu.Unlock()
}

// owns говорит, относится ли действие к Spotify прямо сейчас. sp — Spotify звучит, y — звучит музыка Яндекса.
func (c *Component) owns(a action, ps playerState) bool {
	sp := c.pcm != nil && c.pcm.Playing() || ps.Status == "playing"
	y := c.mach != nil && c.mach.Playing()
	c.mu.Lock()
	owner := c.owner
	c.mu.Unlock()
	// Spotify на паузе и ничего другого не играет: сессия ещё его (выбрана в приложении), если Яндекс не играл позже
	paused := !sp && !y && ps.Status == "paused" && ps.Active && owner != ownerYandex
	switch a {
	case actPause:
		return sp && !y
	case actResume:
		return paused
	case actNext, actPrev:
		return sp && !y || paused
	}
	return false
}

// InterceptPhrase вызывает демон для каждой окончательной фразы (интерфейс демона phraseInterceptor). true — фраза выполнена здесь,
// ответ облака голосом гасится. Не блокирует: Soloist принимает команды по WebSocket без ожидания результата.
func (c *Component) InterceptPhrase(reqID, text string, fast bool) bool {
	cfg := c.config()
	if !cfg.Voice || c.ctl == nil {
		return false
	}
	a := parsePhrase(text)
	if a == actNone {
		return false
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn || !c.owns(a, ps) {
		return false
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
		c.logf("голос: «%s» не выполнена: %v — фраза уходит Алисе", a, err)
		return false
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
	if c.mach != nil && (a == actNext || a == actPrev || a == actResume) {
		// платформа сама запустит очередь Яндекса (audio_play после «дальше», «назад», «играй»): этот запуск достанется Spotify
		c.mach.ArmCarrier(carrierWindow)
	}
	if cfg.VoiceCancel && !fast { // ответ быстрой команды локальный и беззвучный, а его анимацию пользователь ждёт
		go c.cancelDialog()
	}
	return true
}

// directiveActions — директивы облака, которыми платформа управляет плеером. На короткие фразы без слова «Алиса» («пауза», «дальше»
// во время музыки) текста локально нет, зато облако присылает директиву: её имя и есть команда.
var directiveActions = map[string]action{
	"player_pause":          actPause,
	"player_continue":       actResume,
	"player_next_track":     actNext,
	"player_previous_track": actPrev,
}

// directiveEcho — сколько после перехваченной фразы директива того же действия считается повтором: фразу уже выполнили, а облако,
// если отмена диалога не успела, всё равно ответило.
const directiveEcho = 6 * time.Second

// OnDirective получает имя каждой директивы ответа Алисы (интерфейс демона directiveHandler). Если директива управляет плеером и
// Spotify сейчас хозяин звука, действие выполняется в Soloist. Платформа выполнит её сама лишь для своих плееров (музыка Яндекса).
func (c *Component) OnDirective(name string) {
	a, ok := directiveActions[name]
	if !ok {
		return
	}
	cfg := c.config()
	if !cfg.Voice || c.ctl == nil {
		return
	}
	ps := c.ctl.state()
	if !ps.Connected || !ps.LoggedIn || !c.owns(a, ps) {
		return
	}
	c.mu.Lock()
	echo := c.voiceAct == a && time.Since(c.voiceAt) < directiveEcho
	c.mu.Unlock()
	if echo {
		return
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
		c.logf("голос: директива %s не выполнена: %v", name, err)
		return
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = a.String() + " (директива облака)"
	c.voiceAt = time.Now()
	c.voiceAct = a
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
	Last    string    `json:"last,omitempty"`
	LastAt  time.Time `json:"last_at,omitzero"`
	Owner   string    `json:"owner,omitempty"` // кто последним начал играть
}

func (c *Component) voiceStatus() voiceStatus {
	cfg := c.config()
	c.mu.Lock()
	defer c.mu.Unlock()
	return voiceStatus{Enabled: cfg.Voice, Cancel: cfg.VoiceCancel, Handled: c.voiceHandled, Last: c.voiceLast, LastAt: c.voiceAt, Owner: c.owner}
}

// defaultCancel — отмена диалога через aliced (порт 9875), как кнопка 'c' в консоли maind.
func defaultCancel(ctx context.Context) error {
	return (&alicedclient.Client{}).Cancel(ctx)
}
