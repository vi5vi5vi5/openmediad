package yandexmusic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/components/yandex-music/aac"
	"openmediad/internal/components/yandex-music/mp4"
	"openmediad/internal/core/audio"
)

const (
	bytesPerMs   = 192               // PCM 48 кГц × 2 канала × 2 байта / 1000
	ringBytes    = 4000 * bytesPerMs // запас PCM между декодером и выводом: 4 с (давление доходит до декодера и скачивания)
	historyBytes = 1500 * bytesPerMs // история для возврата звука при паузе: 1,5 с
	openLevel    = 300 * bytesPerMs  // «поток открыт», когда готовы 300 мс звука
	skipLimit    = 256 << 10         // дальше этого от текущего места поток открывается заново (Range), ближе — читается насквозь
	maxRetries   = 2                 // повторов чтения кадра (с перебором зеркал)
	retryPause   = 200 * time.Millisecond
)

// Track — что нужно для потока: ссылки, ключ, название (для журнала).
type Track struct {
	ID    string
	URL   string
	Alts  []string // зеркала (поле 1.42)
	Key   []byte   // AES-128, 16 байт
	Title string
}

// Deps — внешние зависимости источника (подменяются в тестах).
type Deps struct {
	Client     *http.Client
	NewDecoder aac.Factory
	Logf       func(string, ...any)
}

// Source — один трек Яндекса как источник движка. Сессия = (декодер, буфер, скачивание) с позиции; перемотка бросает
// сессию и начинает новую.
type Source struct {
	d       Deps
	t       Track
	startMs uint64

	got atomic.Uint64 // скачано байт (diag conbytes)
	tot atomic.Int64  // длина потока (diag conconlen)

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond // сигнал о смене сессии
	idx     *mp4.Index
	sess    *session
	closed  bool
	woken   bool  // Wake, пока сессии нет: ближайшее чтение вернёт audio.ErrWake
	eof     bool  // сессию после перемотки запустить не удалось: звука больше не будет
	opened  bool  // первые 300 мс звука были готовы
	bootErr error // ошибка до открытия: её вернёт Open
}

type session struct {
	cancel context.CancelFunc
	ring   *audio.Ring
	dec    aac.Decoder
	baseMs uint64 // позиция первого кадра сессии
}

// New готовит источник трека с позиции startMs (возобновление и перемотка); сеть и декодер — в Open.
func New(t Track, startMs uint64, d Deps) *Source {
	if d.Client == nil {
		d.Client = NewClient()
	}
	if d.NewDecoder == nil {
		d.NewDecoder = aac.NewGst
	}
	s := &Source{d: d, t: t, startMs: startMs}
	s.cond = sync.NewCond(&s.mu)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

func (s *Source) logf(f string, a ...any) {
	if s.d.Logf != nil {
		s.d.Logf("трек %s (%s): %s", short(s.t.ID), s.t.Title, fmt.Sprintf(f, a...))
	}
}

func short(id string) string { return id[:min(8, len(id))] }

func (s *Source) urls() []string { return append([]string{s.t.URL}, s.t.Alts...) }

// Format — PCM декодера: 48 кГц стерео.
func (s *Source) Format() audio.Format { return audio.Stereo48 }

// Open открывает поток, читает таблицу moov, запускает первую сессию и ждёт первые 300 мс звука.
func (s *Source) Open(ctx context.Context) (audio.Info, error) {
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	body, err := Fetch(s.ctx, s.d.Client, s.urls(), s.t.Key, 0, &s.got)
	if err != nil {
		return audio.Info{}, fmt.Errorf("открытие потока: %w", err)
	}
	idx, err := mp4.ReadMoov(body)
	if err != nil {
		body.Close()
		return audio.Info{}, err
	}
	s.tot.Store(body.Total)
	s.mu.Lock()
	s.idx = idx
	s.mu.Unlock()
	sess, err := s.start(body, s.startMs)
	if err != nil {
		return audio.Info{}, err
	}
	if !sess.ring.WaitLevel(openLevel) {
		return audio.Info{}, errors.New("источник закрыт")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return audio.Info{}, errors.New("источник закрыт")
	}
	if s.bootErr != nil && sess.ring.Len() < openLevel {
		return audio.Info{}, s.bootErr
	}
	s.opened = true
	return audio.Info{Start: time.Duration(sess.baseMs) * time.Millisecond, Duration: time.Duration(idx.DurationMs) * time.Millisecond}, nil
}

// start создаёт сессию с позиции posMs. hdr — уже открытое тело потока сразу после moov (или nil).
func (s *Source) start(hdr *Body, posMs uint64) (*session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if hdr != nil {
			hdr.Close()
		}
		return nil, errors.New("источник закрыт")
	}
	idx := s.idx
	i0 := idx.SampleAtMs(posMs)
	if posMs >= idx.DurationMs {
		i0 = idx.N()
	}
	s.mu.Unlock()
	dec, err := s.d.NewDecoder()
	if err != nil {
		if hdr != nil {
			hdr.Close()
		}
		err = fmt.Errorf("запуск декодера: %w", err)
		s.failed(err)
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	ss := &session{cancel: cancel, ring: audio.NewRing(ringBytes, historyBytes), dec: dec, baseMs: idx.StartMs(min(i0, idx.N()-1))}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.drop(ss)
		if hdr != nil {
			hdr.Close()
		}
		return nil, errors.New("источник закрыт")
	}
	s.sess, s.eof = ss, false
	s.cond.Broadcast()
	s.mu.Unlock()
	go s.pump(ss)
	go s.feed(ctx, ss, hdr, i0)
	return ss, nil
}

// failed — ошибка источника. До открытия её вернёт Open (откат на mediad); после — трек считается законченным: буфер
// доигрывается, и движок получает конец звука (очередь платформы пойдёт дальше).
func (s *Source) failed(err error) {
	s.mu.Lock()
	closed, opened, sess := s.closed, s.opened, s.sess
	if !opened && s.bootErr == nil {
		s.bootErr = err
	}
	s.mu.Unlock()
	if closed {
		return
	}
	s.logf("%v", err)
	if sess != nil {
		sess.ring.CloseWrite()
		return
	}
	s.mu.Lock()
	s.eof = true // новой сессии не будет: чтение должно кончиться, а не ждать вечно
	s.cond.Broadcast()
	s.mu.Unlock()
}

// drop останавливает сессию: буфер, запросы, декодер (процесс гасится в фоне, чтобы не задерживать вызывающего).
func (s *Source) drop(ss *session) {
	ss.cancel()
	ss.ring.Stop()
	go ss.dec.Close()
}

// pump переливает PCM из декодера в буфер.
func (s *Source) pump(ss *session) {
	n, err := io.Copy(ss.ring, ss.dec)
	if err != nil && !errors.Is(err, audio.ErrStopped) {
		s.logf("чтение PCM: %v", err)
	}
	if n == 0 && !errors.Is(err, audio.ErrStopped) {
		tail := ""
		if g, ok := ss.dec.(interface{ Tail() string }); ok {
			tail = g.Tail()
		}
		s.mu.Lock()
		cur := s.sess == ss && !s.closed
		s.mu.Unlock()
		if cur {
			s.failed(fmt.Errorf("декодер не выдал звук: %.300s", tail))
		}
	}
	ss.ring.CloseWrite()
}

// feed режет поток на кадры AAC по таблице moov и отдаёт декодеру в обёртке ADTS.
func (s *Source) feed(ctx context.Context, ss *session, hdr *Body, i0 int) {
	defer ss.dec.CloseWrite()
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	body, pos := hdr, int64(-1)
	if hdr != nil {
		pos = int64(idx.MoovEnd)
	}
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	var maxSz uint32
	for _, z := range idx.Size {
		maxSz = max(maxSz, z)
	}
	buf := make([]byte, 7+int(maxSz))
	for i := i0; i < idx.N(); i++ {
		off, size := int64(idx.Off[i]), int(idx.Size[i])
		for attempt := 0; ; attempt++ {
			if ctx.Err() != nil {
				return
			}
			var err error
			if body == nil || off < pos || off-pos > skipLimit {
				if body != nil {
					body.Close()
					body = nil
				}
				body, err = Fetch(ctx, s.d.Client, s.urls(), s.t.Key, off, &s.got)
				if err == nil {
					pos = off
				}
			} else if off > pos {
				if _, err = io.CopyN(io.Discard, body, off-pos); err == nil {
					pos = off
				}
			}
			if err == nil {
				_, err = io.ReadFull(body, buf[7:7+size])
			}
			if err == nil {
				pos += int64(size)
				break
			}
			if body != nil {
				body.Close()
				body = nil
			}
			if ctx.Err() != nil {
				return
			}
			if attempt >= maxRetries {
				s.failed(fmt.Errorf("чтение кадра %d: %w", i, err))
				return
			}
			s.logf("кадр %d: %v — повтор", i, err)
			time.Sleep(retryPause)
		}
		h, err := aac.ADTS(idx.ASC, size)
		if err != nil {
			s.failed(err)
			return
		}
		copy(buf, h[:])
		if _, err := ss.dec.Write(buf[:7+size]); err != nil {
			return
		}
	}
}

// Read отдаёт PCM текущей сессии. Во время перемотки ждёт новую сессию; после Wake возвращает audio.ErrWake.
func (s *Source) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		for s.sess == nil && !s.closed && !s.woken && !s.eof {
			s.cond.Wait()
		}
		switch {
		case s.closed:
			s.mu.Unlock()
			return 0, audio.ErrStopped
		case s.sess == nil && s.eof:
			s.mu.Unlock()
			return 0, io.EOF
		case s.woken:
			s.woken = false
			s.mu.Unlock()
			return 0, audio.ErrWake
		}
		ss := s.sess
		s.mu.Unlock()
		n, err := ss.ring.ReadWait(p)
		if errors.Is(err, audio.ErrStopped) {
			s.mu.Lock()
			replaced, closed := s.sess != ss, s.closed
			s.mu.Unlock()
			if replaced && !closed {
				continue // перемотка: читаем новую сессию
			}
		}
		return n, err
	}
}

// Wake прерывает ожидающее чтение (пауза, закрытие).
func (s *Source) Wake() {
	s.mu.Lock()
	ss := s.sess
	if ss == nil {
		s.woken = true
		s.cond.Broadcast()
	}
	s.mu.Unlock()
	if ss != nil {
		ss.ring.Wake()
	}
}

// Rewind возвращает в буфер n последних отданных байт (звук, не дошедший до динамика при паузе).
func (s *Source) Rewind(n int) int {
	s.mu.Lock()
	ss := s.sess
	s.mu.Unlock()
	if ss == nil {
		return 0
	}
	return ss.ring.Rewind(n)
}

// Seek бросает текущую сессию и начинает новую с кадра, на который приходится pos (Range-запрос). Возвращает начало
// этого кадра.
func (s *Source) Seek(pos time.Duration) (time.Duration, error) {
	posMs := uint64(pos / time.Millisecond)
	s.mu.Lock()
	if s.closed || s.idx == nil {
		s.mu.Unlock()
		return 0, errors.New("источник не открыт")
	}
	idx := s.idx
	old := s.sess
	s.sess = nil
	s.mu.Unlock()
	if old != nil {
		s.drop(old)
	}
	i0 := idx.SampleAtMs(posMs)
	at := idx.StartMs(i0)
	if posMs >= idx.DurationMs {
		at = idx.DurationMs
	}
	go s.start(nil, posMs)
	return time.Duration(at) * time.Millisecond, nil
}

// Close освобождает всё: скачивание, декодер, буфер.
func (s *Source) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ss := s.sess
	s.sess = nil
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
	if ss != nil {
		s.drop(ss)
	}
	return nil
}

// Net — сколько байт скачано и какова длина потока (идёт в диагностику событий плеера).
func (s *Source) Net() (got, total uint64) {
	return s.got.Load(), uint64(max(s.tot.Load(), 0))
}

// Buffered — сколько звука в буфере (для отладочной страницы).
func (s *Source) Buffered() time.Duration {
	s.mu.Lock()
	ss := s.sess
	s.mu.Unlock()
	if ss == nil {
		return 0
	}
	return time.Duration(ss.ring.Len()/bytesPerMs) * time.Millisecond
}
