package alicevoice

import (
	"sync"
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

	mu     sync.Mutex
	cur    *reply
	closed bool
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
	r.st = b.Engine.NewStream(id, engine.Voice, r.src, engine.Options{Spectrum: spectrum.SourceVoice}, engine.Events{
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
}
