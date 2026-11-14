package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// Плейлисты по образцу настоящей радиостанции (главный с тремя вариантами AAC, медиа — живое окно сегментов MPEG-TS).
const masterSample = `#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=68400,AVERAGE-BANDWIDTH=67900,CODECS="mp4a.40.5"
64/playlist.m3u8?hlssid=abc
#EXT-X-STREAM-INF:BANDWIDTH=116400,AVERAGE-BANDWIDTH=115900,CODECS="mp4a.40.2"
112/playlist.m3u8?hlssid=abc
#EXT-X-STREAM-INF:BANDWIDTH=36400,AVERAGE-BANDWIDTH=35900,CODECS="mp4a.40.29"
32/playlist.m3u8?hlssid=abc
`

const mediaSample = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:41
#EXT-X-PROGRAM-DATE-TIME:2026-11-14T18:09:55.093Z
#EXT-X-DISCONTINUITY
#EXTINF:5.990748,
l0_a.ts
#EXTINF:5.990748,title
https://cdn.example.com/abs/l0_b.ts?x=1
#EXTINF:6,
../up/l0_c.ts
`

func TestParse(t *testing.T) {
	m, err := Parse(masterSample, mustURL("https://radio.example.com/silver/playlist.m3u8"))
	if err != nil || !m.Master() || len(m.Variants) != 3 {
		t.Fatalf("главный плейлист: %+v, %v", m, err)
	}
	best, _ := m.Best()
	if best.Bandwidth != 116400 || best.Codecs != "mp4a.40.2" || best.URL != "https://radio.example.com/silver/112/playlist.m3u8?hlssid=abc" {
		t.Fatalf("выбран вариант %+v", best)
	}

	p, err := Parse(mediaSample, mustURL("https://radio.example.com/silver/112/playlist.m3u8?hlssid=abc"))
	if err != nil || p.Master() || p.Ended || p.Target != 6*time.Second || len(p.Segments) != 3 {
		t.Fatalf("медиа-плейлист: %+v, %v", p, err)
	}
	want := []Segment{
		{"https://radio.example.com/silver/112/l0_a.ts", 41, 5990748 * time.Microsecond},
		{"https://cdn.example.com/abs/l0_b.ts?x=1", 42, 5990748 * time.Microsecond},
		{"https://radio.example.com/silver/up/l0_c.ts", 43, 6 * time.Second},
	}
	for i, w := range want {
		if p.Segments[i] != w {
			t.Errorf("сегмент %d: %+v, ждали %+v", i, p.Segments[i], w)
		}
	}
	if p, _ := Parse(mediaSample+"#EXT-X-ENDLIST\n", mustURL("http://x/p.m3u8")); !p.Ended {
		t.Error("EXT-X-ENDLIST не замечен")
	}

	// видео рядом со звуком: выбирается вариант без видео, даже если он «уже»
	mixed := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,CODECS=\"avc1.4d401f,mp4a.40.2\"\nv.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=96000,CODECS=\"mp4a.40.2\"\na.m3u8\n"
	if m, _ := Parse(mixed, mustURL("http://x/m.m3u8")); m.Variants[0].Codecs != "avc1.4d401f,mp4a.40.2" {
		t.Fatalf("кодеки с запятой в кавычках разобраны как %q", m.Variants[0].Codecs)
	} else if best, _ := m.Best(); best.URL != "http://x/a.m3u8" {
		t.Fatalf("при видео выбран %+v", best)
	}

	for name, text := range map[string]string{
		"не плейлист":     "<html>404</html>",
		"пустой":          "#EXTM3U\n",
		"шифрованный":     "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:6,\na.ts\n",
		"fMP4":            "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\na.m4s\n",
		"негодная ссылка": "#EXTM3U\n#EXTINF:6,\nhttp://[::1\n",
	} {
		if p, err := Parse(text, mustURL("http://x/p.m3u8")); err == nil {
			t.Errorf("%s: принят: %+v", name, p)
		}
	}
	if _, err := Parse("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:6,\na.ts\n", mustURL("http://x/p.m3u8")); err != nil {
		t.Errorf("METHOD=NONE отвергнут: %v", err)
	}
	if _, err := Parse("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n#EXTINF:6,\na.ts\n", mustURL("http://x/p.m3u8")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("шифрование — не ErrUnsupported: %v", err)
	}
}

// ---- MPEG-TS для тестов ----

// tsPacketOf собирает пакет TS: данных меньше 184 байт — остальное занимает поле адаптации.
func tsPacketOf(pid int, start bool, cc int, payload []byte) []byte {
	p := make([]byte, 0, tsPacket)
	b1 := byte(pid >> 8 & 0x1F)
	if start {
		b1 |= 0x40
	}
	p = append(p, 0x47, b1, byte(pid))
	if pad := 184 - len(payload); pad > 0 {
		p = append(p, 0x30|byte(cc&0xF), byte(pad-1))
		if pad > 1 {
			p = append(p, 0x00)
			p = append(p, bytes.Repeat([]byte{0xFF}, pad-2)...)
		}
	} else {
		p = append(p, 0x10|byte(cc&0xF))
	}
	return append(p, payload...)
}

func tsTables(pmtPID, audioPID int, streamType byte) []byte {
	pat := []byte{0x00, 0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00, 0x00, 0x01, 0xE0 | byte(pmtPID>>8), byte(pmtPID), 0, 0, 0, 0}
	pmt := []byte{0x00, 0x02, 0xB0, 0x12, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE0 | byte(audioPID>>8), byte(audioPID), 0xF0, 0x00,
		streamType, 0xE0 | byte(audioPID>>8), byte(audioPID), 0xF0, 0x00, 0, 0, 0, 0}
	return append(tsPacketOf(0, true, 0, pat), tsPacketOf(pmtPID, true, 0, pmt)...)
}

// tsSegment упаковывает элементарный поток es в сегмент MPEG-TS: таблицы, затем пакеты PES по pesSize байт данных; между
// звуковыми пакетами вставлен чужой поток (его данные в выход попасть не должны).
func tsSegment(es []byte, streamType byte, pesSize int) []byte {
	const pmtPID, audioPID, otherPID = 0x100, 0x101, 0x102
	seg := tsTables(pmtPID, audioPID, streamType)
	cc := 0
	for len(es) > 0 {
		n := min(pesSize, len(es))
		pes := append([]byte{0, 0, 1, 0xC0, byte((n + 8) >> 8), byte(n + 8), 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}, es[:n]...)
		es = es[n:]
		for first := true; len(pes) > 0; first = false {
			k := min(184, len(pes))
			seg = append(seg, tsPacketOf(audioPID, first, cc, pes[:k])...)
			pes = pes[k:]
			cc++
		}
		seg = append(seg, tsPacketOf(otherPID, true, cc, bytes.Repeat([]byte{0xAB}, 100))...)
	}
	return seg
}

// adts — n кадров ADTS по size байт (AAC-LC 44,1 кГц стерео), данные кадров различимы.
func adts(n, size int, mark byte) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		l := size
		out = append(out, 0xFF, 0xF1, 0x50, 0x80|byte(l>>11), byte(l>>3), byte(l&7)<<5|0x1F, 0xFC)
		out = append(out, bytes.Repeat([]byte{mark + byte(i)}, size-7)...)
	}
	return out
}

// checkADTS проверяет, что b — цепочка целых кадров ADTS, и возвращает их число.
func checkADTS(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		if len(b) < 7 || b[0] != 0xFF || b[1]&0xF6 != 0xF0 {
			return n, fmt.Errorf("кадр %d: нет синхрослова ADTS (осталось %d байт)", n, len(b))
		}
		l := int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5])>>5
		if l < 7 || l > len(b) {
			return n, fmt.Errorf("кадр %d: длина %d при остатке %d", n, l, len(b))
		}
		b = b[l:]
		n++
	}
	return n, nil
}

func TestExtractTS(t *testing.T) {
	es := adts(40, 371, 1)
	var e Extractor
	// сегмент 1: кадры режутся на пакеты PES произвольно; сегмент 2 продолжает тот же поток
	out1, err := e.Push(tsSegment(es[:9000], 0x0F, 1000))
	if err != nil {
		t.Fatal(err)
	}
	out2, err := e.Push(tsSegment(es[9000:], 0x0F, 733))
	if err != nil {
		t.Fatal(err)
	}
	if got := append(out1, out2...); !bytes.Equal(got, es) || e.Codec() != CodecAAC {
		t.Fatalf("извлечено %d байт из %d, кодек %v", len(got), len(es), e.Codec())
	}
	if n, err := checkADTS(es); n != 40 || err != nil {
		t.Fatalf("проверка самого теста: %d кадров, %v", n, err)
	}

	var mp3 Extractor
	if out, err := mp3.Push(tsSegment([]byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3}, 0x03, 100)); err != nil || mp3.Codec() != CodecMP3 || len(out) != 7 {
		t.Fatalf("MP3 в TS: %x, %v, %v", out, mp3.Codec(), err)
	}
	var none Extractor
	if _, err := none.Push(tsSegment([]byte{1, 2, 3}, 0x1B, 100)); !errors.Is(err, ErrUnsupported) { // только видео H.264
		t.Fatalf("TS без звука: %v", err)
	}
	var broken Extractor
	seg := tsSegment(es[:2000], 0x0F, 500)
	seg[3*tsPacket] = 0x00
	if _, err := broken.Push(seg); err == nil {
		t.Fatal("сбой синхронизации не замечен")
	}
}

func TestExtractRaw(t *testing.T) {
	frames := adts(3, 200, 7)
	id3 := append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 63}, bytes.Repeat([]byte{0x55}, 63)...)
	var e Extractor
	out, err := e.Push(append(append([]byte{}, id3...), frames...))
	if err != nil || !bytes.Equal(out, frames) || e.Codec() != CodecAAC {
		t.Fatalf("AAC с меткой ID3: %d байт, %v, %v", len(out), e.Codec(), err)
	}
	if out, _ := e.Push(frames); !bytes.Equal(out, frames) {
		t.Fatal("сегмент без метки изменён")
	}
	var mp3 Extractor
	if _, err := mp3.Push([]byte{0xFF, 0xFB, 0x90, 0x00, 1, 2}); err != nil || mp3.Codec() != CodecMP3 {
		t.Fatalf("MP3: %v, %v", mp3.Codec(), err)
	}
	for name, seg := range map[string][]byte{
		"fMP4":  append([]byte{0, 0, 0, 24, 's', 't', 'y', 'p'}, make([]byte, 16)...),
		"мусор": []byte("<html>ошибка</html>"),
	} {
		var x Extractor
		if _, err := x.Push(seg); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ---- живой поток ----

// station — поддельная радиостанция: окно из последних сегментов, как у настоящей.
type station struct {
	mu       sync.Mutex
	first    int // номер первого сегмента в окне
	count    int // сколько сегментов в окне
	ended    bool
	gone     map[int]bool // сегменты, на которые сервер отвечает 404
	requests atomic.Int64
	srv      *httptest.Server
}

func newStation(t *testing.T, first, count int) *station {
	st := &station{first: first, count: count, gone: map[int]bool{}}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.requests.Add(1)
		st.mu.Lock()
		defer st.mu.Unlock()
		switch {
		case r.URL.Path == "/radio/playlist.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=64000,CODECS=\"mp4a.40.5\"\nlow/list.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS=\"mp4a.40.2\"\nhigh/list.m3u8?sid=1\n")
		case r.URL.Path == "/radio/high/list.m3u8":
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", st.first)
			for i := st.first; i < st.first+st.count; i++ {
				fmt.Fprintf(w, "#EXTINF:2,\nseg%d.ts\n", i)
			}
			if st.ended {
				fmt.Fprint(w, "#EXT-X-ENDLIST\n")
			}
		case strings.HasPrefix(r.URL.Path, "/radio/high/seg"):
			var n int
			fmt.Sscanf(r.URL.Path, "/radio/high/seg%d.ts", &n)
			if st.gone[n] || n < st.first || n >= st.first+st.count {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, "segment-%d", n)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *station) advance(by int) {
	st.mu.Lock()
	st.first += by
	st.mu.Unlock()
}

func (st *station) open(t *testing.T) *Stream {
	t.Helper()
	s, err := Open(context.Background(), st.srv.URL+"/radio/playlist.m3u8", Options{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func next(t *testing.T, s *Stream) string {
	t.Helper()
	b, err := s.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return string(b)
}

func TestStreamLive(t *testing.T) {
	st := newStation(t, 100, 8)
	var got atomic.Uint64
	s, err := Open(context.Background(), st.srv.URL+"/radio/playlist.m3u8", Options{Got: &got, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	// живой поток начинается за три сегмента до края: 105, 106, 107
	for _, want := range []string{"segment-105", "segment-106", "segment-107"} {
		if got := next(t, s); got != want {
			t.Fatalf("получен %s, ждали %s", got, want)
		}
	}
	if got.Load() == 0 {
		t.Fatal("счётчик скачанного не растёт")
	}
	// дальше сегментов нет: Next ждёт и перечитывает плейлист, пока станция не выдаст новый
	waits := 0
	s.sleep = func(context.Context, time.Duration) error {
		if waits++; waits == 2 {
			st.advance(1) // окно сдвинулось: появился 108
		}
		return nil
	}
	if got := next(t, s); got != "segment-108" || waits != 2 {
		t.Fatalf("после ожидания: %s, ожиданий %d", got, waits)
	}

	// долгая пауза: окно ушло далеко вперёд — поток встаёт у нового края, а не ждёт пропавший сегмент
	st.advance(50)
	if got := next(t, s); got != "segment-156" {
		t.Fatalf("после отставания: %s", got)
	}
	// станция начала нумерацию заново
	st.mu.Lock()
	st.first = 0
	st.mu.Unlock()
	next(t, s) // 157 ещё не было: перечитывание покажет окно 0–7
	if s.next < 5 || s.next > 8 {
		t.Fatalf("после сброса нумерации следующий сегмент %d", s.next)
	}
}

func TestStreamErrors(t *testing.T) {
	// один сегмент не скачался — пропускаем и идём дальше
	st := newStation(t, 0, 8)
	st.gone[6] = true
	s := st.open(t)
	s.sleep = func(context.Context, time.Duration) error { return nil }
	if a, b := next(t, s), next(t, s); a != "segment-5" || b != "segment-7" {
		t.Fatalf("пропуск сегмента: %s, %s", a, b)
	}

	// поток замер: плейлист не обновляется — ошибка, а не вечное ожидание
	var slept time.Duration
	s.sleep = func(_ context.Context, d time.Duration) error { slept += d; return nil }
	if _, err := s.Next(context.Background()); err == nil || !strings.Contains(err.Error(), "остановился") || slept < stall-5*time.Second {
		t.Fatalf("замерший поток: %v после %v", err, slept)
	}

	// запись (EXT-X-ENDLIST): с первого сегмента до конца, затем io.EOF
	vod := newStation(t, 0, 3)
	vod.ended = true
	v := vod.open(t)
	for i := 0; i < 3; i++ {
		if got := next(t, v); got != fmt.Sprintf("segment-%d", i) {
			t.Fatalf("запись: %s", got)
		}
	}
	if _, err := v.Next(context.Background()); err != io.EOF {
		t.Fatalf("конец записи: %v", err)
	}

	// отмена прерывает ожидание
	live := newStation(t, 0, 4).open(t)
	for i := 0; i < 3; i++ {
		next(t, live)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := live.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("отмена: %v", err)
	}

	// сервера нет; адрес негодный; ссылка в тексте ошибки не светится
	if _, err := Open(context.Background(), "http://127.0.0.1:1/secret-token/playlist.m3u8", Options{}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("недоступный сервер: %v", err)
	}
	if _, err := Open(context.Background(), "не адрес", Options{}); err == nil {
		t.Fatal("негодный адрес принят")
	}
	if _, err := Open(context.Background(), st.srv.URL+"/нет.m3u8", Options{}); err == nil {
		t.Fatal("404 на плейлист принят")
	}
}

// Живая проверка на настоящей станции: OMD_HLS_URL=https://…/playlist.m3u8 go test -run TestLiveStation -v ./internal/libs/hls
// Читает три сегмента и проверяет, что извлечённый звук — целые кадры ADTS. OMD_HLS_DUMP=файл сохраняет его (для декодера).
func TestLiveStation(t *testing.T) {
	u := os.Getenv("OMD_HLS_URL")
	if u == "" {
		t.Skip("живая проверка: задайте OMD_HLS_URL (адрес плейлиста радиостанции)")
	}
	s, err := Open(context.Background(), u, Options{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	var e Extractor
	var all []byte
	for i := 0; i < 3; i++ {
		seg, err := s.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		es, err := e.Push(seg)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("сегмент %d: %d байт, звука %d байт, кодек %v", i, len(seg), len(es), e.Codec())
		all = append(all, es...)
	}
	if e.Codec() == CodecAAC {
		n, err := checkADTS(all)
		// поток режется на сегменты по границам PES, но последний кадр может быть неполным только при обрыве: ждём целые
		if err != nil {
			t.Fatalf("после %d кадров: %v", n, err)
		}
		t.Logf("кадров ADTS: %d", n)
	}
	if f := os.Getenv("OMD_HLS_DUMP"); f != "" {
		os.WriteFile(f, all, 0o644)
	}
}
