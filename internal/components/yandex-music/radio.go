package yandexmusic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/libs/aac"
	"openmediad/internal/libs/gstreamer"
	"openmediad/internal/libs/hls"
)

// Радио. «Алиса, включи радио» приходит той же командой плеера музыки, что и трек, но вместо файла MP4 с ключом в ней
// ссылка на живой поток HLS без ключа (сегменты MPEG-TS с AAC). Ссылка бывает прямо на плейлист станции (….m3u8), а бывает
// на адрес Яндекса (api.music.yandex.net/radio-stream/…), который перенаправляет на плейлист: по виду ссылки радио не узнать. Источник качает сегменты по мере появления
// (libs/hls), достаёт из них звук и отдаёт тому же декодеру GStreamer, что и треки.
//
// У живого потока нет длительности и перемотки; пауза придерживает звук в буфере, а после долгой паузы поток продолжается
// с того, что станция передаёт сейчас.

// IsRadio — трек из команды платформы не файл музыки Яндекса, а поток без ключа (радиостанция). Музыка Яндекса всегда
// зашифрована, поэтому признак — отсутствие ключа; HLS это или нет, выяснится при открытии (не HLS — ошибка до первого
// звука, и поток сыграет штатный плеер).
func IsRadio(t Track) bool {
	if len(t.Key) != 0 {
		return false
	}
	u, err := url.Parse(t.URL)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// mp3Pipeline — конвейер для станций, вещающих в MP3 (элементы есть во вшитом GStreamer колонки: ими играются звонки).
var mp3Pipeline = "fdsrc fd=0 ! mpegaudioparse ! avdec_mp3 ! " + gstreamer.RawOut(48000, 2)

type gstMP3 struct{ p *gstreamer.Process }

// newGstMP3 запускает декодер MP3 на GStreamer колонки.
func newGstMP3() (aac.Decoder, error) {
	p, err := gstreamer.Start(mp3Pipeline, true)
	if err != nil {
		return nil, err
	}
	return &gstMP3{p}, nil
}

func (d *gstMP3) Write(b []byte) (int, error) { return d.p.In.Write(b) }
func (d *gstMP3) Read(b []byte) (int, error)  { return d.p.Out.Read(b) }
func (d *gstMP3) CloseWrite() error           { return d.p.In.Close() }
func (d *gstMP3) Close() error                { return d.p.Close() }
func (d *gstMP3) Tail() string                { return d.p.Tail() }

// RadioSource — радиостанция (живой поток HLS) как источник движка.
type RadioSource struct {
	d Deps
	t Track

	got atomic.Uint64 // скачано байт (диагностика событий плеера)

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	ring    *audio.Ring
	dec     aac.Decoder
	closed  bool
	opened  bool  // первые 300 мс звука были готовы
	bootErr error // ошибка до открытия: её вернёт Open (и радио сыграет штатный плеер)
}

// NewRadio готовит источник радиостанции; сеть и декодер — в Open.
func NewRadio(t Track, d Deps) *RadioSource {
	if d.Client == nil {
		d.Client = NewClient()
	}
	if d.NewDecoder == nil {
		d.NewDecoder = aac.NewGst
	}
	if d.NewMP3Decoder == nil {
		d.NewMP3Decoder = newGstMP3
	}
	s := &RadioSource{d: d, t: t, ring: audio.NewRing(ringBytes, historyBytes)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

func (s *RadioSource) logf(f string, a ...any) {
	if s.d.Logf != nil {
		s.d.Logf("радио %s (%s): %s", short(s.t.ID), strings.TrimLeft(s.t.Title, " —-"), fmt.Sprintf(f, a...))
	}
}

// Format — PCM декодера: 48 кГц стерео.
func (s *RadioSource) Format() audio.Format { return audio.Stereo48 }

// Open открывает поток станции, по первому сегменту узнаёт кодек, запускает декодер и ждёт первые 300 мс звука.
func (s *RadioSource) Open(ctx context.Context) (audio.Info, error) {
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	st, err := hls.Open(s.ctx, s.t.URL, hls.Options{Client: s.d.Client, Got: &s.got, Logf: s.logf})
	if err != nil {
		return audio.Info{}, fmt.Errorf("открытие потока радио: %w", err)
	}
	var ex hls.Extractor
	first, err := s.segment(st, &ex)
	if err != nil {
		return audio.Info{}, fmt.Errorf("первый сегмент радио: %w", err)
	}
	var dec aac.Decoder
	switch ex.Codec() {
	case hls.CodecAAC:
		dec, err = s.d.NewDecoder()
	case hls.CodecMP3:
		dec, err = s.d.NewMP3Decoder()
	default:
		err = errors.New("кодек потока не определён")
	}
	if err != nil {
		return audio.Info{}, fmt.Errorf("запуск декодера радио: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		go dec.Close()
		return audio.Info{}, errors.New("источник закрыт")
	}
	s.dec = dec
	s.mu.Unlock()
	s.logf("поток открыт, звук %v", ex.Codec())
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
	return audio.Info{}, nil // длительность неизвестна: живой поток
}

// segment читает следующий сегмент и достаёт из него звук.
func (s *RadioSource) segment(st *hls.Stream, ex *hls.Extractor) ([]byte, error) {
	seg, err := st.Next(s.ctx)
	if err != nil {
		return nil, err
	}
	return ex.Push(seg)
}

// feed подаёт декодеру звук сегмент за сегментом. Темп задаёт воспроизведение: буфер PCM полон — декодер перестаёт
// читать, и следующий сегмент не скачивается.
func (s *RadioSource) feed(st *hls.Stream, ex *hls.Extractor, dec aac.Decoder, es []byte) {
	defer dec.CloseWrite()
	for {
		if _, err := dec.Write(es); err != nil {
			return // декодер закрыт (Close) или умер: об этом скажет pump
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

// pump переливает PCM из декодера в буфер.
func (s *RadioSource) pump(dec aac.Decoder) {
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

// failed — ошибка потока. До открытия её вернёт Open (и радио сыграет штатный плеер); после — вещание считается
// законченным: буфер доигрывается, и движок получает конец звука.
func (s *RadioSource) failed(err error) {
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

// Read отдаёт PCM; после Wake возвращает audio.ErrWake.
func (s *RadioSource) Read(p []byte) (int, error) { return s.ring.ReadWait(p) }

// Wake прерывает ожидающее чтение (пауза, закрытие).
func (s *RadioSource) Wake() { s.ring.Wake() }

// Rewind возвращает в буфер n последних отданных байт (звук, не дошедший до динамика при паузе).
func (s *RadioSource) Rewind(n int) int { return s.ring.Rewind(n) }

// Close освобождает всё: скачивание, декодер, буфер.
func (s *RadioSource) Close() error {
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
		go dec.Close() // процесс гасится в фоне, чтобы не задерживать вызывающего
	}
	return nil
}

// Net — сколько байт скачано; длина живого потока неизвестна.
func (s *RadioSource) Net() (got, total uint64) { return s.got.Load(), 0 }

// Buffered — сколько звука в буфере (для отладочной страницы).
func (s *RadioSource) Buffered() time.Duration {
	return time.Duration(s.ring.Len()/bytesPerMs) * time.Millisecond
}
