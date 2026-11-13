package alicevoice

import (
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/spectrum"
)

// Привязка к эмуляции mediad: Backend реализует mediad.VoiceBackend — ответ Алисы (voice-start, voice-chunk, voice-eos)
// становится потоком движка класса voice. Один ответ за раз: новый вытесняет прежний.

// VoiceEvents — куда Backend сообщает о результатах (это mediad.Machine).
type VoiceEvents interface {
	VoicePlaying(id string)
	VoiceEnded(id string)
	VoiceFailed(id string, err error)
}

// Backend — голос Алисы поверх движка.
type Backend struct {
	Engine *engine.Engine
	Events VoiceEvents
	Shared *Shared // общий декодер; nil — создаётся сам
	Logf   func(string, ...any)

	// keeper (необязательный) держит громкость колонки на время ответов (answervol.go); nil — как решит платформа.
	// Меняется на ходу настройкой answer_volume (Reload).
	keeper atomic.Pointer[answerKeeper]

	mu     sync.Mutex
	cur    *reply
	closed bool
}

// OnFocus — диалог с Алисой начался или кончился (демон сообщает всем компонентам).
func (b *Backend) OnFocus(on bool) {
	if k := b.keeper.Load(); k != nil {
		k.OnFocus(on)
	}
}

// OnReply — ответ облака на запрос: по его директивам видно, просил ли человек поменять громкость.
func (b *Backend) OnReply(_ string, directives []string) {
	if k := b.keeper.Load(); k != nil {
		k.OnReply(directives)
	}
}

// DebugState — состояние удержания громкости ответов для status -v.
func (b *Backend) DebugState() any {
	k := b.keeper.Load()
	if k == nil {
		return map[string]any{"answer_volume": AnswerVolumePlatform}
	}
	return map[string]any{"answer_volume": AnswerVolumeHold, "hold": k.status()}
}

// setAnswerVolume включает или выключает удержание громкости ответов. Регулятора нет (не колонка, другая прошивка) — голос всё равно
// работает, просто без удержания.
func (b *Backend) setAnswerVolume(mode string) {
	if mode != AnswerVolumeHold {
		if k := b.keeper.Swap(nil); k != nil {
			k.Close()
			b.logf("громкость ответов: как решит колонка (answer_volume = \"platform\")")
		}
		return
	}
	if b.keeper.Load() != nil {
		return
	}
	mc, err := openMaster()
	if err != nil {
		b.logf("громкость ответов не удерживается: регулятор громкости колонки недоступен (%v)", err)
		return
	}
	k := newAnswerKeeper(mc, b.Logf)
	if !b.keeper.CompareAndSwap(nil, k) {
		k.Close()
		return
	}
	b.logf("громкость ответов удерживается на уровне до ответа (answer_volume = \"hold\")")
}

func (b *Backend) logf(f string, a ...any) {
	if b.Logf != nil {
		b.Logf(f, a...)
	}
}

// Reload применяет изменённые настройки на ходу: answer_volume переключается без перезапуска движка.
func (b *Backend) Reload(cfg any) (restart []string) {
	c, ok := cfg.(*Config)
	if !ok || c == nil {
		return nil
	}
	if err := c.validate(); err != nil {
		b.logf("новые настройки голоса отвергнуты: %v", err)
		return nil
	}
	b.setAnswerVolume(c.AnswerVolume)
	return nil
}

type reply struct {
	id  string
	st  *engine.Stream
	src *Source
}

var _ mediad.VoiceBackend = (*Backend)(nil)

func (b *Backend) current(id string) *reply {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != nil && b.cur.id == id {
		return b.cur
	}
	return nil
}

// Start готовит вывод ответа: aplay поднимается сразу, чтобы первый звук не ждал его запуска (≈14 мс).
func (b *Backend) Start(id string) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if b.Shared == nil {
		b.Shared = &Shared{}
	}
	r := &reply{id: id, src: NewSource(b.Shared, b.Logf)}
	r.st = b.Engine.NewStream(id, engine.Voice, r.src, engine.Options{Spectrum: spectrum.SourceVoice, WaitTail: true}, engine.Events{
		Playing: func() { b.Events.VoicePlaying(id) },
		Ended: func() {
			b.mu.Lock()
			if b.cur == r {
				b.cur = nil
			}
			b.mu.Unlock()
			b.Events.VoiceEnded(id)
		},
		Failed: func(err error) {
			b.mu.Lock()
			if b.cur == r {
				b.cur = nil
			}
			b.mu.Unlock()
			b.Events.VoiceFailed(id, err)
		},
	})
	old := b.cur
	b.cur = r
	b.mu.Unlock()
	if old != nil {
		old.st.Close()
	}
	r.st.Play()
}

// Chunk передаёт кусок Ogg/Opus очередного voice-chunk.
func (b *Backend) Chunk(id string, ogg []byte) {
	if r := b.current(id); r != nil {
		r.src.Push(ogg)
	}
}

// EOS — чанков больше не будет (voice-eos): после доигрывания придёт VoiceEnded.
func (b *Backend) EOS(id string) {
	if r := b.current(id); r != nil {
		r.src.EOS()
	}
}

// Abort обрывает ответ сразу (перебили, release раньше конца, остановка демона). Событий после этого нет.
func (b *Backend) Abort(id string) {
	b.mu.Lock()
	r := b.cur
	if r == nil || r.id != id {
		b.mu.Unlock()
		return
	}
	b.cur = nil
	b.mu.Unlock()
	r.st.Close()
}

// Pos — позиция ответа, мс (по часам, не по записанным байтам).
func (b *Backend) Pos(id string) uint64 {
	if r := b.current(id); r != nil {
		return uint64(r.st.Pos() / time.Millisecond)
	}
	return 0
}

// Shutdown обрывает текущий ответ и больше не принимает новые.
func (b *Backend) Shutdown() {
	b.mu.Lock()
	b.closed = true
	r := b.cur
	b.cur = nil
	b.mu.Unlock()
	if r != nil {
		r.st.Close()
	}
	if k := b.keeper.Swap(nil); k != nil {
		k.Close()
	}
}
