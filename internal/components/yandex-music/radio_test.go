package yandexmusic

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/components/yandex-music/aac"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
)

const radioBlock = 96000 // PCM на один сегмент у поддельного декодера радио: полсекунды

// radioDec «декодирует» радио: на каждый сегмент отдаёт radioBlock байт PCM, в первом отсчёте — номер сегмента.
type radioDec struct {
	pr *io.PipeReader
	pw *io.PipeWriter
}

func newRadioDec() *radioDec {
	pr, pw := io.Pipe()
	return &radioDec{pr: pr, pw: pw}
}

func (d *radioDec) Write(p []byte) (int, error) {
	pcm := make([]byte, radioBlock)
	binary.LittleEndian.PutUint16(pcm, uint16(p[len(p)-1])) // последний байт сегмента — его номер
	if _, err := d.pw.Write(pcm); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (d *radioDec) Read(p []byte) (int, error) { return d.pr.Read(p) }
func (d *radioDec) CloseWrite() error          { return d.pw.Close() }
func (d *radioDec) Close() error {
	d.pw.CloseWithError(io.ErrClosedPipe)
	d.pr.CloseWithError(io.ErrClosedPipe)
	return nil
}

// radioStation — поддельная станция: главный плейлист, медиа-плейлист с окном сегментов, сегменты «голого» звука.
type radioStation struct {
	mu      sync.Mutex
	first   int
	count   int
	ended   bool
	media   string  // если задан — отдаётся вместо медиа-плейлиста
	sync    [2]byte // начало сегмента: синхрослово AAC (FF F1) или MP3 (FF FB)
	fetched []int   // какие сегменты скачаны, по порядку
	srv     *httptest.Server
}

func newRadioStation(t *testing.T, first, count int) *radioStation {
	st := &radioStation{first: first, count: count, sync: [2]byte{0xFF, 0xF1}}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		switch {
		case r.URL.Path == "/radio-stream/silver/url": // как у станций Яндекса: адрес из команды перенаправляет на плейлист
			http.Redirect(w, r, "/silver/playlist.m3u8", http.StatusFound)
		case r.URL.Path == "/silver/playlist.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=116400,CODECS=\"mp4a.40.2\"\n112/playlist.m3u8?hlssid=secret\n")
		case r.URL.Path == "/silver/112/playlist.m3u8" && st.media != "":
			fmt.Fprint(w, st.media)
		case r.URL.Path == "/silver/112/playlist.m3u8":
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", st.first)
			for i := st.first; i < st.first+st.count; i++ {
				fmt.Fprintf(w, "#EXTINF:2,\nseg%d.aac\n", i)
			}
			if st.ended {
				fmt.Fprint(w, "#EXT-X-ENDLIST\n")
			}
		case strings.HasPrefix(r.URL.Path, "/silver/112/seg"):
			var n int
			fmt.Sscanf(r.URL.Path, "/silver/112/seg%d.aac", &n)
			st.fetched = append(st.fetched, n)
			w.Write(append(append([]byte{st.sync[0], st.sync[1], 0x50, 0x80}, bytes.Repeat([]byte{0}, 60)...), byte(n)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

type radioHarness struct {
	*harness
	st      *radioStation
	aacDecs atomic.Int32
	mp3Decs atomic.Int32
}

func newRadioHarness(t *testing.T, st *radioStation) *radioHarness {
	h := &radioHarness{harness: &harness{opened: make(chan uint64, 4), failed: make(chan error, 4), done: make(chan string, 4)}, st: st}
	h.eng = &engine.Engine{
		Router: engine.RouterFunc(func(engine.Class, audio.Format) (audio.Output, error) {
			s := &memSink{}
			h.mu.Lock()
			h.sinks = append(h.sinks, s)
			h.mu.Unlock()
			return s, nil
		}),
		Logf: func(f string, a ...any) { t.Logf(f, a...) },
	}
	h.b = &Backend{
		Engine:        h.eng,
		Events:        h.harness,
		Client:        st.srv.Client(),
		NewDecoder:    func() (aac.Decoder, error) { h.aacDecs.Add(1); return newRadioDec(), nil },
		NewMP3Decoder: func() (aac.Decoder, error) { h.mp3Decs.Add(1); return newRadioDec(), nil },
		NoFadeIn:      true,
		NoRewind:      true,
		Logf:          func(f string, a ...any) { t.Logf(f, a...) },
	}
	t.Cleanup(h.b.Shutdown)
	return h
}

// radioTrack — команда платформы «включи радио»: ссылка на плейлист HLS, ключа нет.
func (h *radioHarness) radioTrack() mediad.Track {
	return mediad.Track{ID: "silver_rain", URL: h.st.srv.URL + "/radio-stream/silver/url?streamUuid=secret", Title: " — Серебряный дождь"}
}

// segments — номера сегментов в сыгранном PCM.
func segments(pcm []byte) (out []int) {
	for i := 0; i+radioBlock <= len(pcm); i += radioBlock {
		out = append(out, int(binary.LittleEndian.Uint16(pcm[i:])))
	}
	return
}

func TestIsRadio(t *testing.T) {
	for _, c := range []struct {
		t    Track
		want bool
	}{
		{Track{URL: "https://hls-01.example.ru/silver/playlist.m3u8"}, true},
		{Track{URL: "https://x.example/live/INDEX.M3U8?vsid=1&x=y"}, true},
		{Track{URL: "https://strm.example.net/get/track.mp4?sign=1", Key: []byte("0123456789abcdef")}, false},
		{Track{URL: "https://x.example/playlist.m3u8", Key: []byte("0123456789abcdef")}, false}, // с ключом — не наш случай
		// станция Яндекса: адрес перенаправляет на плейлист, по виду ссылки HLS не узнать
		{Track{URL: "https://api.music.example.net/radio-stream/radio_dacha/url?streamUuid=abc"}, true},
		{Track{URL: "файл.mp4"}, false},
	} {
		if got := IsRadio(c.t); got != c.want {
			t.Errorf("%s: %v", c.t.URL, got)
		}
	}
}

// Радио играет через движок: поток без ключа и без длительности открывается, сегменты идут по порядку с края живого окна.
func TestRadioPlaysLiveStream(t *testing.T) {
	st := newRadioStation(t, 100, 8)
	h := newRadioHarness(t, st)
	h.b.Open("p1", h.radioTrack(), 0)
	if dur := waitFor(t, h.opened, "Opened"); dur != 0 {
		t.Fatalf("у живого потока длительность %d мс", dur)
	}
	if n := len(h.allPCM()); n != 0 {
		t.Fatalf("до Start сыграно %d байт", n)
	}
	h.b.Start("p1")
	deadline := time.Now().Add(5 * time.Second)
	for len(segments(h.allPCM())) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := segments(h.allPCM()); len(got) < 3 || got[0] != 105 || got[1] != 106 || got[2] != 107 {
		t.Fatalf("сыграны сегменты %v, ждали 105 106 107 (три от края окна)", got)
	}
	if h.aacDecs.Load() != 1 || h.mp3Decs.Load() != 0 {
		t.Fatalf("декодеров AAC %d, MP3 %d", h.aacDecs.Load(), h.mp3Decs.Load())
	}
	if got, total := h.b.Net("p1"); got == 0 || total != 0 {
		t.Fatalf("скачано %d из %d: длины у живого потока нет", got, total)
	}
	if snap := h.b.Snapshot(); len(snap) != 1 || !snap[0].Playing || !snap[0].Opened || !strings.Contains(snap[0].Title, "Серебряный дождь") {
		t.Fatalf("снимок: %+v", snap)
	}
	for deadline := time.Now().Add(2 * time.Second); h.b.Pos("p1") == 0; time.Sleep(10 * time.Millisecond) { // позиция идёт по часам
		if time.Now().After(deadline) {
			t.Fatal("позиция не растёт")
		}
	}
	// пауза и продолжение: источник тот же, поток не открывается заново
	h.b.Pause("p1")
	h.b.Start("p1")
	h.b.Close("p1")
	select {
	case err := <-h.failed:
		t.Fatalf("радио сообщило об ошибке: %v", err)
	default:
	}
	if h.aacDecs.Load() != 1 {
		t.Fatalf("после паузы декодеров %d", h.aacDecs.Load())
	}
}

// Вещание кончилось (станция закрыла поток): буфер доигрывается, платформа получает «закончено».
func TestRadioEndsWhenStreamEnds(t *testing.T) {
	st := newRadioStation(t, 0, 3)
	st.ended = true
	h := newRadioHarness(t, st)
	h.b.Open("p1", h.radioTrack(), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	if id := waitFor(t, h.done, "Finished"); id != "p1" {
		t.Fatalf("Finished для %q", id)
	}
	if got := segments(h.allPCM()); len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Fatalf("сыграны сегменты %v", got)
	}
}

// Станция в MP3: запускается декодер MP3, а не AAC.
func TestRadioMP3Station(t *testing.T) {
	st := newRadioStation(t, 0, 4)
	st.sync = [2]byte{0xFF, 0xFB}
	h := newRadioHarness(t, st)
	h.b.Open("p1", h.radioTrack(), 0)
	waitFor(t, h.opened, "Opened")
	if h.aacDecs.Load() != 0 || h.mp3Decs.Load() != 1 {
		t.Fatalf("декодеров AAC %d, MP3 %d", h.aacDecs.Load(), h.mp3Decs.Load())
	}
}

// Поток, который движок играть не умеет (шифрованный, fMP4) или который не открылся: ошибка до первого звука — автомат
// отдаст команду штатному плееру, и радио сыграет он. В тексте ошибки нет ссылки (в ней метка сессии).
func TestRadioFailsBeforeSound(t *testing.T) {
	for name, media := range map[string]string{
		"шифрованные сегменты": "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:2,\nseg1.aac\n",
		"fMP4":        "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2,\nseg1.m4s\n",
		"не плейлист": "<html>станция переехала</html>",
	} {
		st := newRadioStation(t, 0, 4)
		st.media = media
		h := newRadioHarness(t, st)
		h.b.Open("p1", h.radioTrack(), 0)
		err := waitFor(t, h.failed, name)
		if err == nil || strings.Contains(err.Error(), "hlssid") || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: ошибка %v", name, err)
		}
		if h.aacDecs.Load()+h.mp3Decs.Load() != 0 {
			t.Errorf("%s: декодер запущен зря", name)
		}
	}
	// сервер станции недоступен
	st := newRadioStation(t, 0, 4)
	h := newRadioHarness(t, st)
	tr := h.radioTrack()
	st.srv.Close()
	h.b.Open("p1", tr, 0)
	if err := waitFor(t, h.failed, "недоступная станция"); err == nil || strings.Contains(err.Error(), "streamUuid") {
		t.Fatalf("недоступная станция: %v", err)
	}
}
