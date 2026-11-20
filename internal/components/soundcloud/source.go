package soundcloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/libs/aac"
	"openmediad/internal/libs/hls"
)

// Звук трека: плейлист HLS (запись, сегменты fMP4 по 10 с) → libs/hls достаёт кадры AAC → декодер AAC (GStreamer колонки, 48 кГц стерео)
// → буфер PCM, из которого читает движок. Темп задаёт воспроизведение: буфер полон — декодер не читает, следующий сегмент не качается.

const (
	bytesPerMs   = 192               // PCM 48 кГц × 2 канала × 2 байта / 1000
	ringBytes    = 4000 * bytesPerMs // запас PCM между декодером и выводом: 4 с
	historyBytes = 1500 * bytesPerMs // история для возврата звука при паузе: 1,5 с
	openLevel    = 300 * bytesPerMs  // «трек открыт», когда готовы 300 мс звука
)

// trackSource — трек SoundCloud как источник движка.
type trackSource struct {
	url    string // плейлист HLS (с подписью: в журнал не попадает)
	dur    time.Duration
	client *http.Client
	newDec aac.Factory
	logf   func(string, ...any)

	got atomic.Uint64 // скачано байт

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	ring    *audio.Ring
	dec     aac.Decoder
	closed  bool
	opened  bool
	bootErr error
}

func newTrackSource(streamURL string, dur time.Duration, client *http.Client, newDec aac.Factory, logf func(string, ...any)) *trackSource {
	s := &trackSource{url: streamURL, dur: dur, client: client, newDec: newDec, logf: logf, ring: audio.NewRing(ringBytes, historyBytes)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

// Format — PCM декодера: 48 кГц стерео.
func (s *trackSource) Format() audio.Format { return audio.Stereo48 }

// Open открывает плейлист, по первому сегменту запускает декодер и ждёт первые 300 мс звука.
func (s *trackSource) Open(ctx context.Context) (audio.Info, error) {
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	st, err := hls.Open(s.ctx, s.url, hls.Options{Client: s.client, Got: &s.got, Logf: s.logf})
	if err != nil {
		return audio.Info{}, fmt.Errorf("поток трека не открылся: %w", err)
	}
	var ex hls.Extractor
	first, err := s.segment(st, &ex)
	if err != nil {
		return audio.Info{}, fmt.Errorf("первый сегмент трека: %w", err)
	}
	if ex.Codec() != hls.CodecAAC {
		return audio.Info{}, fmt.Errorf("звук трека не AAC (%v)", ex.Codec())
	}
	dec, err := s.newDec()
	if err != nil {
		return audio.Info{}, fmt.Errorf("запуск декодера AAC: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		go dec.Close()
		return audio.Info{}, errors.New("источник закрыт")
	}
	s.dec = dec
	s.mu.Unlock()
	go s.pump(dec)
	go s.feed(st, &ex, dec, first)

	if !s.ring.WaitLevel(openLevel) {
		return audio.Info{}, errors.New("источник закрыт")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return audio.Info{}, errors.New("источник закрыт")
	}
	if s.bootErr != nil && s.ring.Len() < openLevel {
		return audio.Info{}, s.bootErr
	}
	s.opened = true
	return audio.Info{Duration: s.dur}, nil
}

func (s *trackSource) segment(st *hls.Stream, ex *hls.Extractor) ([]byte, error) {
	seg, err := st.Next(s.ctx)
	if err != nil {
		return nil, err
	}
	return ex.Push(seg)
}

// feed подаёт декодеру звук сегмент за сегментом до конца трека.
func (s *trackSource) feed(st *hls.Stream, ex *hls.Extractor, dec aac.Decoder, es []byte) {
	defer dec.CloseWrite()
	for {
		if _, err := dec.Write(es); err != nil {
			return // декодер закрыт или умер: об этом скажет pump
		}
		var err error
		if es, err = s.segment(st, ex); err != nil {
			if s.ctx.Err() == nil && err != io.EOF {
				s.failed(err)
			}
			return
		}
	}
}

// pump переливает PCM из декодера в буфер; конец декодера — конец звука трека.
func (s *trackSource) pump(dec aac.Decoder) {
	n, err := io.Copy(s.ring, dec)
	if err != nil && !errors.Is(err, audio.ErrStopped) {
		s.logf("чтение PCM: %v", err)
	}
	if n == 0 && !errors.Is(err, audio.ErrStopped) {
		tail := ""
		if g, ok := dec.(interface{ Tail() string }); ok {
			tail = g.Tail()
		}
		s.failed(fmt.Errorf("декодер не выдал звук: %.300s", tail))
	}
	s.ring.CloseWrite()
}

// failed — ошибка потока: до открытия её вернёт Open, после — трек считается доигранным (буфер доигрывается).
func (s *trackSource) failed(err error) {
	s.mu.Lock()
	closed := s.closed
	if !s.opened && s.bootErr == nil {
		s.bootErr = err
	}
	s.mu.Unlock()
	if closed {
		return
	}
	s.logf("%v", err)
	s.ring.CloseWrite()
}

func (s *trackSource) Read(p []byte) (int, error) { return s.ring.ReadWait(p) }
func (s *trackSource) Wake()                      { s.ring.Wake() }
func (s *trackSource) Rewind(n int) int           { return s.ring.Rewind(n) }

// Close освобождает скачивание, декодер и буфер.
func (s *trackSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	dec := s.dec
	s.mu.Unlock()
	s.cancel()
	s.ring.Stop()
	if dec != nil {
		go dec.Close()
	}
	return nil
}
