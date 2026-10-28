// Package tts — источник «голос Алисы»: ответы приходят командами voice-chunk как самостоятельные потоки Ogg/Opus (моно,
// 48 кГц, CELT), декодируются на чистом Go (codec/opus) и идут в движок классом voice. И привязка к эмуляции mediad (bind.go).
//
// Декодер один на всё время работы (≈3 МБ): новый на каждый ответ раздувал бы кучу (замер на колонке,
// docs/research/HISTORY.md, раздел 13); между ответами — Reset.
package tts

import (
	"context"
	"io"
	"sync"

	"openmediad/internal/audio"
	"openmediad/internal/codec/opus"
)

// Decoder — декодер Ogg/Opus: куски потока на входе, PCM S16LE моно 48 кГц на выходе.
type Decoder interface {
	Reset()
	Write(chunk []byte, sink func(pcm []byte) error) error
}

// NewOpusDecoder создаёт настоящий декодер. tap получает выброшенные pre-skip отсчёты (их штатный путь отдаёт спектру).
func NewOpusDecoder(tap func(pcm []byte)) (Decoder, error) {
	s, err := opus.New()
	if err != nil {
		return nil, err
	}
	s.PreSkipTap = tap
	return s, nil
}

// Shared — общий декодер для всех ответов. Нулевое значение готово к работе (декодер создаётся при первом ответе).
type Shared struct {
	// NewDecoder (необязательный) подменяет декодер в тестах.
	NewDecoder func(tap func(pcm []byte)) (Decoder, error)

	mu    sync.Mutex
	dec   Decoder
	owner *Source // ответ, который сейчас пользуется декодером: смена владельца — Reset
}

// decode раскодирует чанк для src (под общей блокировкой: декодер один).
func (sh *Shared) decode(src *Source, chunk []byte, sink func(pcm []byte) error) error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.dec == nil {
		mk := sh.NewDecoder
		if mk == nil {
			mk = NewOpusDecoder
		}
		d, err := mk(func(pcm []byte) {
			if sh.owner != nil {
				sh.owner.tapped(pcm)
			}
		})
		if err != nil {
			return err
		}
		sh.dec = d
	}
	if sh.owner != src {
		sh.dec.Reset()
		sh.owner = src
	}
	return sh.dec.Write(chunk, sink)
}

type opKind uint8

const (
	opChunk opKind = iota
	opEOS
)

type op struct {
	kind opKind
	data []byte
}

// Source — один ответ Алисы. Push и EOS кладут команды в очередь; Read (в горутине вывода движка) их раскодирует.
type Source struct {
	sh   *Shared
	logf func(string, ...any)

	ops       chan op
	closeOnce sync.Once
	closeCh   chan struct{}

	mu       sync.Mutex
	pending  []byte // раскодированный, ещё не отданный PCM
	produced bool   // звук уже был: битый чанк дальше пропускается, а не ломает ответ
	tap      func(pcm []byte)
}

// NewSource создаёт источник одного ответа.
func NewSource(sh *Shared, logf func(string, ...any)) *Source {
	return &Source{sh: sh, logf: logf, ops: make(chan op, 256), closeCh: make(chan struct{})}
}

// Format — моно 48 кГц (стерео из него делает plug ALSA).
func (s *Source) Format() audio.Format { return audio.Mono48 }

// Open ничего не ждёт: звук придёт чанками.
func (s *Source) Open(context.Context) (audio.Info, error) { return audio.Info{}, nil }

// Push добавляет чанк Ogg/Opus (ogg принадлежит источнику).
func (s *Source) Push(ogg []byte) { s.enqueue(op{opChunk, ogg}) }

// EOS — чанков больше не будет: после доигрывания Read вернёт io.EOF.
func (s *Source) EOS() { s.enqueue(op{kind: opEOS}) }

func (s *Source) enqueue(o op) {
	select {
	case s.ops <- o:
	default:
		if s.logf != nil {
			s.logf("голос: очередь чанков переполнена, кусок отброшен")
		}
	}
}

// Read отдаёт PCM; когда раскодированное кончилось, берёт и раскодирует следующий чанк.
func (s *Source) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()
		var o op
		select {
		case <-s.closeCh:
			return 0, audio.ErrStopped
		case o = <-s.ops:
		}
		if o.kind == opEOS {
			return 0, io.EOF
		}
		var got []byte
		err := s.sh.decode(s, o.data, func(pcm []byte) error {
			got = append(got, pcm...)
			return nil
		})
		s.mu.Lock()
		s.pending = append(s.pending[:0], got...)
		if len(got) > 0 {
			s.produced = true
		}
		produced := s.produced
		s.mu.Unlock()
		if err != nil {
			if !produced {
				return 0, err // до первого звука: ответ отдаётся штатному mediad
			}
			if s.logf != nil {
				s.logf("голос: чанк пропущен: %v", err) // посреди ответа портить его целиком незачем
			}
		}
	}
}

// SetSpectrumTap — отвод для спектра (pre-skip Opus); его ставит движок.
func (s *Source) SetSpectrumTap(f func(pcm []byte)) {
	s.mu.Lock()
	s.tap = f
	s.mu.Unlock()
}

func (s *Source) tapped(pcm []byte) {
	s.mu.Lock()
	f := s.tap
	s.mu.Unlock()
	if f != nil {
		f(pcm)
	}
}

// Close прерывает ожидание чанков.
func (s *Source) Close() error {
	s.closeOnce.Do(func() { close(s.closeCh) })
	return nil
}
