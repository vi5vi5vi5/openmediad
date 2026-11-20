package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync/atomic"
	"time"
)

const (
	maxPlaylist = 1 << 20  // плейлист больше мегабайта — что-то не то
	maxSegment  = 16 << 20 // сегмент звука — десятки и сотни килобайт; предел от ошибочных ссылок
	// liveBack — с какого сегмента от конца начинать живой поток: спецификация советует три длительности от края,
	// чтобы следующий сегмент наверняка появился раньше, чем доиграется текущий.
	liveBack = 3
	// stall — сколько живой поток может не давать новых сегментов (или не отдавать плейлист), прежде чем он считается умершим.
	stall = 30 * time.Second
	// maxSkips — сколько сегментов подряд можно пропустить из-за ошибок скачивания.
	maxSkips = 3
)

// Options — параметры Open.
type Options struct {
	Client *http.Client // nil — http.DefaultClient
	// Got, если задан, получает число скачанных байт (плейлисты и сегменты).
	Got *atomic.Uint64
	// Logf — журнал (без ссылок: в них бывают метки сессии).
	Logf func(format string, a ...any)
}

// Stream — открытый поток HLS: Next отдаёт сегменты по порядку, для живого потока — по мере появления.
type Stream struct {
	o     Options
	c     *http.Client
	media *url.URL // адрес медиа-плейлиста
	list  *Playlist
	next  uint64 // номер сегмента, который отдаст следующий Next
	skips int
	init  string // сегмент инициализации fMP4, уже отданный вместе с сегментом (его адрес)
	sleep func(context.Context, time.Duration) error
}

// Open читает плейлист по адресу rawurl (главный — выбирает вариант и читает его медиа-плейлист) и встаёт на начало:
// у живого потока — за три сегмента до края, у записи — на первый сегмент.
func Open(ctx context.Context, rawurl string, o Options) (*Stream, error) {
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return nil, errors.New("hls: негодный адрес потока")
	}
	c := http.DefaultClient
	if o.Client != nil {
		c = o.Client
	}
	if c.Jar == nil { // станции привязывают сессию слушателя к куке: своя банка на поток
		jar, _ := cookiejar.New(nil)
		cc := *c
		cc.Jar = jar
		c = &cc
	}
	s := &Stream{o: o, c: c, sleep: sleepCtx}
	list, u, err := s.load(ctx, u)
	if err != nil {
		return nil, err
	}
	if list.Master() {
		v, _ := list.Best()
		if u, err = url.Parse(v.URL); err != nil {
			return nil, errors.New("hls: негодная ссылка на вариант потока")
		}
		if list, u, err = s.load(ctx, u); err != nil {
			return nil, err
		}
		if list.Master() {
			return nil, errors.New("hls: вариант потока — снова список вариантов")
		}
		s.logf("вариант %d кбит/с (%s)", v.Bandwidth/1000, v.Codecs)
	}
	if len(list.Segments) == 0 {
		return nil, errors.New("hls: в плейлисте нет сегментов")
	}
	s.media, s.list = u, list
	s.next = list.Segments[0].Seq
	if !list.Ended && len(list.Segments) > liveBack {
		s.next = list.Segments[len(list.Segments)-liveBack].Seq
	}
	return s, nil
}

func (s *Stream) logf(f string, a ...any) {
	if s.o.Logf != nil {
		s.o.Logf(f, a...)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// statusError — сервер ответил не 200.
type statusError int

func (e statusError) Error() string {
	return fmt.Sprintf("hls: сервер ответил %d", int(e))
}

// get скачивает ресурс; final — его адрес после перенаправлений.
func (s *Stream) get(ctx context.Context, u string, limit int64) (body []byte, final *url.URL, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := s.c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) { // без ссылки в тексте ошибки
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("hls: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, statusError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if s.o.Got != nil {
		s.o.Got.Add(uint64(len(b)))
	}
	if err != nil {
		return nil, nil, fmt.Errorf("hls: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, nil, errors.New("hls: ответ сервера слишком большой")
	}
	return b, resp.Request.URL, nil
}

// load скачивает и разбирает плейлист. Возвращает и его настоящий адрес: до плейлиста могут вести перенаправления (так
// устроены станции Яндекса), а относительные ссылки внутри считаются от того адреса, по которому он в итоге отдан.
func (s *Stream) load(ctx context.Context, u *url.URL) (*Playlist, *url.URL, error) {
	b, final, err := s.get(ctx, u.String(), maxPlaylist)
	if err != nil {
		return nil, nil, err
	}
	list, err := Parse(string(b), final)
	return list, final, err
}

// Next возвращает следующий сегмент целиком. Для живого потока ждёт, пока он появится в плейлисте. io.EOF — запись
// кончилась (EXT-X-ENDLIST); другая ошибка — поток умер (плейлист не обновляется или сегменты не скачиваются).
//
// У потока fMP4 перед первым сегментом (и когда EXT-X-MAP сменился) идёт сегмент инициализации: Next отдаёт их одним куском
// (инициализация, затем сегмент), и Extractor читает из инициализации, чем сжат звук.
func (s *Stream) Next(ctx context.Context) ([]byte, error) {
	var idle time.Duration
	for {
		if seg, ok := s.find(); ok {
			data, _, err := s.get(ctx, seg.URL, maxSegment)
			if err == nil && seg.Map != "" && seg.Map != s.init {
				var init []byte
				if init, _, err = s.get(ctx, seg.Map, maxSegment); err == nil {
					s.init = seg.Map
					data = append(init, data...)
				}
			}
			if err == nil {
				s.next, s.skips = seg.Seq+1, 0
				return data, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if s.skips++; s.skips > maxSkips {
				return nil, fmt.Errorf("сегменты не скачиваются: %w", err)
			}
			var se statusError
			if errors.As(err, &se) && (se == http.StatusNotFound || se == http.StatusGone) && !s.list.Ended {
				// сегмента на сервере уже нет. Если плейлист устарел (долгая пауза, окно ушло вперёд), свежий плейлист
				// поставит нас у края; если сегмент в нём всё ещё значится — это сбой сервера, пропускаем один сегмент.
				if err := s.reload(ctx); err != nil && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if s.next != seg.Seq {
					continue
				}
			}
			s.logf("сегмент %d: %v — пропускаю", seg.Seq, err)
			s.next = seg.Seq + 1
			continue
		}
		if s.list.Ended {
			return nil, io.EOF
		}
		// новых сегментов пока нет: ждём половину длительности сегмента и перечитываем плейлист
		wait := min(max(s.list.Target/2, time.Second), 5*time.Second)
		if idle += wait; idle > stall {
			return nil, errors.New("поток остановился: плейлист не обновляется")
		}
		if err := s.sleep(ctx, wait); err != nil {
			return nil, err
		}
		if err := s.reload(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logf("плейлист: %v — повтор", err)
		}
	}
}

// find ищет в плейлисте сегмент с номером next.
func (s *Stream) find() (Segment, bool) {
	for _, seg := range s.list.Segments {
		if seg.Seq == s.next {
			return seg, true
		}
	}
	return Segment{}, false
}

// reload перечитывает медиа-плейлист и, если нужного сегмента в нём уже нет (отстали) или нумерация началась заново,
// встаёт у края живого потока.
func (s *Stream) reload(ctx context.Context) error {
	list, _, err := s.load(ctx, s.media)
	if err != nil {
		return err
	}
	if list.Master() || len(list.Segments) == 0 {
		return errors.New("hls: плейлист без сегментов")
	}
	s.list = list
	first, last := list.Segments[0].Seq, list.Segments[len(list.Segments)-1].Seq
	if s.next < first || s.next > last+1 {
		at := list.Segments[max(len(list.Segments)-liveBack, 0)].Seq
		s.logf("отстали от потока или он начался заново (ждали сегмент %d, в плейлисте %d–%d): перехожу к %d", s.next, first, last, at)
		s.next = at
	}
	return nil
}
