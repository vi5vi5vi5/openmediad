// Package pcmstream — источник движка (engine.Source) из звука, который кто-то пишет в него потоком: движок читает, писатель
// блокируется, когда накоплено больше предела. Общий кирпич для компонентов, у которых чужой процесс отдаёт PCM (Spotify:
// Soloist через шим, librespot через канал; приём звука по сети).
//
// Что умеет сверх обычной трубы:
//   - ограниченный буфер: писатель ждёт, пока движок не выберет звук, поэтому чужой процесс не убегает вперёд;
//   - Flush: выбросить накопленное (перемотка, смена трека);
//   - Rewind: вернуть движку последние отданные байты (пауза: звук, который не дошёл до динамика, не пропадает);
//   - Wake: прервать ожидающее чтение (пауза), не теряя данных;
//   - CloseWrite: данных больше не будет; после остатка чтение вернёт io.EOF (конец трека).
//
// Формат всегда S16LE (audio.Format), писать нужно целыми кадрами.
package pcmstream

import (
	"context"
	"io"
	"sync"
	"time"

	"openmediad/internal/core/audio"
)

// Source — буфер между писателем и движком.
type Source struct {
	f       audio.Format
	maxBuf  int // пока накоплено столько и больше, Write ждёт
	histMax int // сколько последних отданных байт помнить для Rewind

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte // buf[off:] — непрочитанное
	off    int
	hist   []byte
	wake   bool
	eof    bool // CloseWrite
	closed bool // Close
	gen    uint64
}

// New создаёт источник. maxBuffered — потолок накопленного звука, history — сколько звука помнить для Rewind.
func New(f audio.Format, maxBuffered, history time.Duration) *Source {
	s := &Source{f: f, maxBuf: f.Bytes(maxBuffered), histMax: f.Bytes(history)}
	if fb := f.FrameBytes(); s.maxBuf < fb {
		s.maxBuf = fb
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Write кладёт звук в буфер. Блокируется, пока буфер полон; Flush, CloseWrite и Close его будят. Возвращает число принятых
// байт; после Close — io.ErrClosedPipe.
func (s *Source) Write(p []byte) (int, error) {
	fb := s.f.FrameBytes()
	done := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	gen := s.gen
	for done < len(p) {
		switch {
		case s.closed || s.eof:
			return done, io.ErrClosedPipe
		case s.gen != gen:
			return len(p), nil // пока ждали, пришёл Flush: остаток устарел
		}
		room := s.maxBuf - (len(s.buf) - s.off)
		room -= room % fb
		if room <= 0 {
			s.cond.Wait()
			continue
		}
		n := len(p) - done
		if n > room {
			n = room
		}
		s.buf = append(s.buf, p[done:done+n]...)
		done += n
		s.cond.Broadcast()
	}
	return done, nil
}

// CloseWrite сообщает, что звука больше не будет.
func (s *Source) CloseWrite() {
	s.mu.Lock()
	s.eof = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Flush выбрасывает накопленное и историю Rewind.
func (s *Source) Flush() {
	s.mu.Lock()
	s.buf, s.off, s.hist = s.buf[:0], 0, s.hist[:0]
	s.gen++
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Buffered — сколько байт накоплено и ещё не прочитано.
func (s *Source) Buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf) - s.off
}

// Format — формат звука (engine.Source).
func (s *Source) Format() audio.Format { return s.f }

// Open ничего не открывает: звук уже идёт (engine.Source).
func (s *Source) Open(context.Context) (audio.Info, error) { return audio.Info{}, nil }

// Read отдаёт целые кадры; ждёт, пока они появятся. io.EOF — после Close, а после CloseWrite — когда остаток выбран;
// audio.ErrWake — по просьбе Wake.
func (s *Source) Read(p []byte) (int, error) {
	fb := s.f.FrameBytes()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		switch {
		case s.closed:
			return 0, io.EOF
		case s.wake:
			s.wake = false
			return 0, audio.ErrWake
		case len(s.buf)-s.off >= fb:
			n := len(s.buf) - s.off
			if n > len(p) {
				n = len(p)
			}
			n -= n % fb
			if n == 0 {
				return 0, nil
			}
			copy(p, s.buf[s.off:s.off+n])
			s.off += n
			s.remember(p[:n])
			if s.off > len(s.buf)/2 && s.off >= 4096 { // не копить прочитанное в начале буфера
				s.buf = append(s.buf[:0], s.buf[s.off:]...)
				s.off = 0
			}
			s.cond.Broadcast()
			return n, nil
		case s.eof:
			return 0, io.EOF
		}
		s.cond.Wait()
	}
}

func (s *Source) remember(p []byte) {
	if s.histMax == 0 {
		return
	}
	s.hist = append(s.hist, p...)
	if extra := len(s.hist) - s.histMax; extra > 0 {
		s.hist = append(s.hist[:0], s.hist[extra:]...)
	}
}

// Wake прерывает ожидающее чтение: оно вернёт audio.ErrWake (engine.Waker).
func (s *Source) Wake() {
	s.mu.Lock()
	s.wake = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Rewind возвращает в начало буфера до n последних отданных байт (целые кадры) и сообщает, сколько вернул (engine.Rewinder).
func (s *Source) Rewind(n int) int {
	fb := s.f.FrameBytes()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.hist) {
		n = len(s.hist)
	}
	n -= n % fb
	if n <= 0 {
		return 0
	}
	back := s.hist[len(s.hist)-n:]
	nb := make([]byte, 0, n+len(s.buf)-s.off)
	nb = append(nb, back...)
	nb = append(nb, s.buf[s.off:]...)
	s.buf, s.off = nb, 0
	s.hist = s.hist[:len(s.hist)-n]
	return n
}

// Close закрывает источник: ожидающие чтение и запись просыпаются (engine.Source).
func (s *Source) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}
