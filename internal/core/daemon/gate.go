package daemon

import (
	"sync"
	"time"

	"openmediad/internal/core/quasar/mediad"
)

// Шлюз голоса. Когда компонент перехватил фразу пользователя («пауза» при играющем Spotify), облако всё равно может ответить на неё
// своим голосом («не поняла»). Дальше две защиты: компонент отменяет диалог (до ответа остаётся около секунды), а шлюз гасит ответ,
// который всё же пришёл: голос не звучит, платформе отправляются те же события (запущен, закончен), чтобы диалог завершился штатно.

// swallowWindow — сколько после перехвата ждём ответ, который нужно погасить: ответ облака приходит через 1–3 секунды после фразы,
// а окно дольше — это риск погасить ответ на следующий вопрос (его новая фраза снимает окно: Clear).
const swallowWindow = 5 * time.Second

// voiceEvents — то, чем шлюз сообщает автомату, что (беззвучный) ответ «прозвучал».
type voiceEvents interface {
	VoicePlaying(id string)
	VoiceEnded(id string)
}

// voiceGate оборачивает бэкенд голоса: пока не включён, прозрачен.
type voiceGate struct {
	inner mediad.VoiceBackend
	m     voiceEvents
	now   func() time.Time

	mu    sync.Mutex
	until time.Time
	muted map[string]*mutedVoice
	count int // сколько ответов погашено
}

type mutedVoice struct{ played bool }

func newVoiceGate(inner mediad.VoiceBackend, m voiceEvents) *voiceGate {
	return &voiceGate{inner: inner, m: m, now: time.Now, muted: map[string]*mutedVoice{}}
}

// Swallow просит погасить ближайший ответ голосом (если он начнётся в течение swallowWindow).
func (g *voiceGate) Swallow() {
	g.mu.Lock()
	g.until = g.now().Add(swallowWindow)
	g.mu.Unlock()
}

// Clear снимает ожидание: пришла новая фраза, которую никто не перехватил, и ответ на неё должен прозвучать.
func (g *voiceGate) Clear() {
	g.mu.Lock()
	g.until = time.Time{}
	g.mu.Unlock()
}

// Swallowed — сколько ответов погашено с запуска.
func (g *voiceGate) Swallowed() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count
}

func (g *voiceGate) mutedFor(id string) *mutedVoice {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.muted[id]
}

func (g *voiceGate) Start(id string) {
	g.mu.Lock()
	if !g.until.IsZero() && g.now().Before(g.until) {
		g.until = time.Time{}
		g.muted[id] = &mutedVoice{}
		g.count++
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	g.inner.Start(id)
}

func (g *voiceGate) Chunk(id string, ogg []byte) {
	mv := g.mutedFor(id)
	if mv == nil {
		g.inner.Chunk(id, ogg)
		return
	}
	g.mu.Lock()
	first := !mv.played
	mv.played = true
	g.mu.Unlock()
	if first {
		go g.m.VoicePlaying(id)
	}
}

func (g *voiceGate) EOS(id string) {
	if g.mutedFor(id) == nil {
		g.inner.EOS(id)
		return
	}
	go func() {
		time.Sleep(40 * time.Millisecond) // после VoicePlaying
		g.m.VoiceEnded(id)
	}()
}

func (g *voiceGate) Abort(id string) {
	g.mu.Lock()
	_, ok := g.muted[id]
	delete(g.muted, id)
	g.mu.Unlock()
	if !ok {
		g.inner.Abort(id)
	}
}

func (g *voiceGate) Pos(id string) uint64 {
	if g.mutedFor(id) != nil {
		return 0
	}
	return g.inner.Pos(id)
}
