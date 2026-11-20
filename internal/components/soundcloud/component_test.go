package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
	"openmediad/internal/core/engine"
	"openmediad/internal/libs/hls"
)

var (
	_ control.Player     = (*Component)(nil)
	_ control.LinkPlayer = (*Component)(nil)
)

const (
	goodID = "GoodClientId0123456789abcdefGH"
	oldID  = "OldClientId0123456789abcdefGHI"
)

// ---- поддельный SoundCloud ----

func tr(id int64, title, user string, opts ...func(map[string]any)) map[string]any {
	t := map[string]any{
		"kind": "track", "id": id, "title": title, "duration": 213886, "policy": "ALLOW", "streamable": true,
		"track_authorization": "auth-" + fmt.Sprint(id), "user": map[string]any{"id": 5, "username": user},
		"media": map[string]any{"transcodings": []map[string]any{
			{"url": "{base}/media/" + fmt.Sprint(id) + "/aac_96k/stream/hls", "preset": "aac_96k", "snipped": false, "quality": "lq",
				"format": map[string]any{"protocol": "hls", "mime_type": `audio/mp4; codecs="mp4a.40.2"`}},
			{"url": "{base}/media/" + fmt.Sprint(id) + "/aac_160k/stream/hls", "preset": "aac_160k", "snipped": false, "quality": "sq",
				"format": map[string]any{"protocol": "hls", "mime_type": `audio/mp4; codecs="mp4a.40.2"`}},
			{"url": "{base}/media/" + fmt.Sprint(id) + "/abr/stream/hls", "preset": "abr_sq", "snipped": false, "quality": "sq",
				"format": map[string]any{"protocol": "hls", "mime_type": "audio/mpegurl"}},
		}},
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// goPlus — у трека есть только отрывок (подписка Go+).
func goPlus(t map[string]any) {
	for _, tc := range t["media"].(map[string]any)["transcodings"].([]map[string]any) {
		tc["snipped"] = true
	}
	t["policy"] = "SNIP"
}

type fakeSC struct {
	t       *testing.T
	srv     *httptest.Server
	validID atomic.Value // какой client_id сейчас годен
	pages   atomic.Int32 // сколько раз открывали главную страницу
	streams sync.Map     // номер трека → сколько раз выдан адрес потока
	broken  sync.Map     // номер трека → адрес потока не выдаётся
	inline  bool         // client_id прямо на странице, а не в скрипте
}

func newFakeSC(t *testing.T) *fakeSC {
	f := &fakeSC{t: t}
	f.validID.Store(goodID)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSC) json(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	w.Write([]byte(strings.ReplaceAll(string(b), "{base}", f.srv.URL)))
}

func (f *fakeSC) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		f.pages.Add(1)
		if f.inline {
			fmt.Fprintf(w, `<html><script>window.__sc_hydration=[{"client_id":"x"}];var a={client_id:"%s"}</script></html>`, f.validID.Load())
			return
		}
		fmt.Fprintf(w, `<html><script crossorigin src="%s/assets/0-aaa.js"></script><script crossorigin src="%s/assets/55-bbb.js"></script>`+
			`<script src="https://evil.example.com/x.js"></script></html>`, f.srv.URL, f.srv.URL)
		return
	case r.URL.Path == "/assets/0-aaa.js":
		io.WriteString(w, `!function(){var e="no id here"}()`)
		return
	case r.URL.Path == "/assets/55-bbb.js":
		fmt.Fprintf(w, `...,{client_id:"%s",env:"production"}...`, f.validID.Load())
		return
	case r.URL.Path == "/short/abc":
		http.Redirect(w, r, "/forss/flickermood", http.StatusFound)
		return
	case r.URL.Path == "/forss/flickermood":
		io.WriteString(w, "<html>страница трека</html>")
		return
	}
	if r.URL.Query().Get("client_id") != f.validID.Load() {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/resolve":
		switch strings.TrimPrefix(r.URL.Query().Get("url"), "https://soundcloud.com") {
		case "/forss/flickermood":
			f.json(w, tr(293, "Flickermood", "Forss"))
		case "/forss/sets/soulhack":
			f.json(w, map[string]any{"kind": "playlist", "title": "Soulhack", "is_album": true, "user": map[string]any{"id": 5, "username": "Forss"},
				"tracks": []any{tr(1, "Raindrops", "Forss"), map[string]any{"kind": "track", "id": 2}, map[string]any{"kind": "track", "id": 3}, map[string]any{"kind": "track", "id": 404}}})
		case "/forss":
			f.json(w, map[string]any{"kind": "user", "id": 5, "username": "Forss"})
		case "/forss/locked":
			f.json(w, tr(7, "Locked", "Forss", goPlus))
		case "/stations/artist/forss":
			f.json(w, map[string]any{"kind": "station", "title": "Станция"})
		default:
			http.NotFound(w, r)
		}
	case r.URL.Path == "/tracks":
		var out []any
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			switch id {
			case "2":
				out = append(out, tr(2, "Flickermood", "Forss"))
			case "3":
				out = append(out, tr(3, "Plus Only", "Forss", goPlus))
			}
		}
		f.json(w, out)
	case r.URL.Path == "/users/5/tracks":
		f.json(w, map[string]any{"collection": []any{tr(10, "New", "Forss"), tr(11, "Old", "Forss")}})
	case strings.HasPrefix(r.URL.Path, "/media/"):
		parts := strings.Split(r.URL.Path, "/") // /media/<id>/<preset>/stream/hls
		if parts[3] != "aac_160k" {
			f.t.Errorf("выбран поток %s, ждали aac_160k", parts[3])
		}
		if _, bad := f.broken.Load(parts[2]); bad {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("track_authorization") != "auth-"+parts[2] {
			f.t.Errorf("track_authorization не передан: %s", r.URL.RawQuery)
		}
		n, _ := f.streams.LoadOrStore(parts[2], new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		f.json(w, map[string]any{"url": "{base}/hls/" + parts[2] + "/playlist.m3u8?Policy=signed"})
	default:
		http.NotFound(w, r)
	}
}

// ---- движок с выводом в память в темпе воспроизведения и поддельный источник ----

type paceOut struct{ f audio.Format }

func (o *paceOut) Write(p []byte) (int, error) {
	time.Sleep(o.f.Duration(uint64(len(p))) / 4)
	return len(p), nil
}
func (o *paceOut) Drain() {}
func (o *paceOut) Abort() {}

// fakeSource — тишина заданной длины вместо HLS и GStreamer; запоминает, какие треки открывались.
type fakeSource struct {
	left   atomic.Int64
	closed atomic.Bool
}

func (s *fakeSource) Format() audio.Format                     { return audio.Stereo48 }
func (s *fakeSource) Open(context.Context) (audio.Info, error) { return audio.Info{}, nil }
func (s *fakeSource) Close() error                             { s.closed.Store(true); return nil }
func (s *fakeSource) Rewind(int) int                           { return 0 }
func (s *fakeSource) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, io.EOF
	}
	n := min(int64(len(p)), s.left.Load())
	if n <= 0 {
		return 0, io.EOF
	}
	s.left.Add(-n)
	clear(p[:n])
	return int(n), nil
}

type stand struct {
	f      *fakeSC
	c      *Component
	mu     sync.Mutex
	opened []string // «номер трека» по порядку открытия
	length time.Duration
}

func newStand(t *testing.T, length time.Duration) *stand {
	s := &stand{f: newFakeSC(t), length: length}
	eng := &engine.Engine{Router: engine.RouterFunc(func(_ engine.Class, f audio.Format) (audio.Output, error) { return &paceOut{f}, nil })}
	s.c = newComponent(*defaults(), eng, s.f.srv.Client(), filepath.Join(t.TempDir(), "soundcloud.json"), t.Logf)
	s.c.api.base, s.c.api.site, s.c.api.assets = s.f.srv.URL, s.f.srv.URL, s.f.srv.URL+"/assets/"
	s.c.newSource = func(tk track, streamURL string) engine.Source {
		if !strings.Contains(streamURL, fmt.Sprintf("/hls/%d/", tk.ID)) {
			t.Errorf("источник трека %d получил чужой адрес %s", tk.ID, streamURL)
		}
		s.mu.Lock()
		s.opened = append(s.opened, fmt.Sprint(tk.ID))
		s.mu.Unlock()
		src := &fakeSource{}
		src.left.Store(int64(audio.Stereo48.Bytes(s.length)))
		return src
	}
	t.Cleanup(s.c.Shutdown)
	return s
}

func (s *stand) link(t *testing.T, link string) (string, error) {
	t.Helper()
	u, err := control.ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	return s.c.PlayLink(context.Background(), u)
}

func (s *stand) openedList() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.opened, ",")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

// ---- тесты ----

// Ссылка на трек: client_id находится на сайте (в скрипте CDN, чужие скрипты не качаются), трек играет и доигрывается.
func TestPlayTrackLink(t *testing.T) {
	s := newStand(t, 300*time.Millisecond)
	title, err := s.link(t, "'https://m.soundcloud.com/forss/flickermood?si=abc&utm_source=clipboard'")
	if err != nil || title != "Forss — Flickermood" {
		t.Fatalf("ответ: %q, %v", title, err)
	}
	waitFor(t, "звук трека", func() bool { return s.c.State().Sounding })
	if st := s.c.State(); !st.Ready || !st.Playing || st.Paused || st.Title != "Forss — Flickermood" {
		t.Fatalf("состояние: %+v", st)
	}
	waitFor(t, "трек доигран", func() bool { return s.c.State() == control.State{} })
	if s.openedList() != "293" || s.f.pages.Load() != 1 {
		t.Fatalf("открыто %s, страница сайта открыта %d раз", s.openedList(), s.f.pages.Load())
	}
	b, err := os.ReadFile(s.c.api.statePath)
	if err != nil || !strings.Contains(string(b), goodID) {
		t.Fatalf("найденный client_id не запомнен: %s, %v", b, err)
	}
	if !strings.Contains(s.c.StatusLine(), "ничего не играет") {
		t.Fatalf("строка статуса: %q", s.c.StatusLine())
	}
}

// Альбом: треки-номера догружаются, трек только для Go+ и ненайденный пропускаются, очередь играет по порядку.
func TestPlaylistQueue(t *testing.T) {
	s := newStand(t, 200*time.Millisecond)
	title, err := s.link(t, "https://soundcloud.com/forss/sets/soulhack/")
	if err != nil || title != "альбом «Soulhack», 2 трека, первым — Forss — Raindrops" {
		t.Fatalf("ответ: %q, %v", title, err)
	}
	waitFor(t, "очередь доиграна", func() bool { return s.openedList() == "1,2" && s.c.State() == control.State{} })

	// исполнитель: его треки
	if title, err := s.link(t, "soundcloud.com/forss"); err != nil || !strings.HasPrefix(title, "треки Forss, 2 трека") {
		t.Fatalf("исполнитель: %q, %v", title, err)
	}
	waitFor(t, "треки исполнителя", func() bool { return strings.HasSuffix(s.openedList(), "10,11") })
}

// Ошибки ссылок: Go+, станция, несуществующее, пустой путь; ничего не запускают.
func TestLinkErrors(t *testing.T) {
	s := newStand(t, time.Second)
	for link, want := range map[string]string{
		"https://soundcloud.com/forss/locked":          "Go+",
		"https://soundcloud.com/stations/artist/forss": "station",
		"https://soundcloud.com/forss/nothing":         "такого нет",
		"https://soundcloud.com/":                      "нет трека",
	} {
		if _, err := s.link(t, link); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (ждали «%s»)", link, err, want)
		}
	}
	if s.openedList() != "" || s.c.State() != (control.State{}) {
		t.Fatalf("после ошибок что-то играет: %s", s.openedList())
	}
	// короткая ссылка раскрывается перенаправлением
	u, _ := url.Parse(s.f.srv.URL + "/short/abc")
	if got, err := s.c.api.expand(context.Background(), u); err != nil || got.Path != "/forss/flickermood" {
		t.Fatalf("короткая ссылка: %v, %v", got, err)
	}
}

// Устаревший client_id (запомненный в файле) — 401: компонент находит новый на сайте и повторяет запрос.
func TestClientIDRefresh(t *testing.T) {
	s := newStand(t, 200*time.Millisecond)
	os.WriteFile(s.c.api.statePath, []byte(`{"client_id":"`+oldID+`","found":"2026-01-01T00:00:00Z"}`), 0o600)
	s.f.inline = true
	if _, err := s.link(t, "soundcloud.com/forss/flickermood"); err != nil {
		t.Fatal(err)
	}
	if s.f.pages.Load() != 1 {
		t.Fatalf("страница сайта открыта %d раз", s.f.pages.Load())
	}
	// client_id из настроек не меняется, даже если не подходит
	fixed := newStand(t, time.Second)
	fixed.c.api.fixedID = oldID
	if _, err := fixed.link(t, "soundcloud.com/forss/flickermood"); err == nil || !strings.Contains(err.Error(), "client_id") || fixed.f.pages.Load() != 0 {
		t.Fatalf("заданный client_id: %v, страница открыта %d раз", err, fixed.f.pages.Load())
	}
}

// Пауза, продолжение, «дальше», «назад» через ядро; договор плеера ядра.
func TestControl(t *testing.T) {
	s := newStand(t, time.Minute)
	controltest.CheckPlayer(t, s.c) // пустой плеер
	if err := s.c.Do(control.Next, control.API); err == nil {
		t.Fatal("«дальше» без очереди выполнено")
	}
	if _, err := s.link(t, "https://soundcloud.com/forss/sets/soulhack"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "звук", func() bool { return s.c.State().Sounding })
	controltest.CheckPlayer(t, s.c) // договор проверяет и паузу: плеер теперь на паузе
	if st := s.c.State(); !st.Paused {
		t.Fatalf("после проверки договора: %+v", st)
	}
	s.c.Do(control.Resume, control.API)
	waitFor(t, "звук после проверки договора", func() bool { return s.c.State().Sounding })

	hub := &control.Hub{Platform: &controltest.Platform{}}
	hub.Add(Name, s.c)
	if !hub.Do(control.Pause, control.Headphones) {
		t.Fatal("пауза не взята")
	}
	if st := s.c.State(); !st.Paused || !st.Resumable || st.Playing || st.Sounding {
		t.Fatalf("после паузы: %+v", st)
	}
	if !hub.Do(control.Toggle, control.Speaker) {
		t.Fatal("продолжение не взято")
	}
	waitFor(t, "снова звучит", func() bool { return s.c.State().Sounding })
	first := s.c.State().Track

	hub.Do(control.Next, control.API)
	waitFor(t, "второй трек", func() bool { return s.c.State().Sounding && s.c.State().Track != first })
	if s.openedList() != "1,2" || !strings.Contains(s.c.StatusLine(), "2 из 2") {
		t.Fatalf("открыто %s, статус %q", s.openedList(), s.c.StatusLine())
	}
	if err := s.c.Do(control.Next, control.API); err == nil {
		t.Fatal("«дальше» за концом очереди выполнено")
	}
	hub.Do(control.Prev, control.API) // в начале трека — предыдущий
	waitFor(t, "снова первый", func() bool { return s.c.State().Track == first && s.c.State().Sounding })
	if err := s.c.Do(control.Toggle, control.API); err == nil {
		t.Fatal("Toggle принят плеером")
	}

	// пауза во время открытия трека: когда он откроется, звука не будет
	s.c.Do(control.Next, control.API)
	s.c.Do(control.Pause, control.API)
	time.Sleep(100 * time.Millisecond)
	if st := s.c.State(); st.Sounding || !st.Paused {
		t.Fatalf("пауза при открытии: %+v", st)
	}
	s.c.Do(control.Resume, control.API)
	waitFor(t, "звук после паузы при открытии", func() bool { return s.c.State().Sounding })
}

// Трек не открылся (нет адреса потока) — следующий; три подряд — очередь стоп.
func TestBrokenTrackSkipped(t *testing.T) {
	s := newStand(t, 200*time.Millisecond)
	s.f.broken.Store("1", true)
	if _, err := s.link(t, "soundcloud.com/forss/sets/soulhack"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "второй трек доигран", func() bool { return s.openedList() == "2" && s.c.State() == control.State{} })
	if !strings.Contains(s.c.StatusLine(), "ничего не играет") {
		t.Fatalf("статус: %q", s.c.StatusLine())
	}
}

func TestConfigAndHelpers(t *testing.T) {
	c := defaults()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Config{{ClientID: "x y", MaxTracks: 10}, {MaxTracks: 0}, {MaxTracks: 501}, {MaxTracks: 10, Proxy: "ftp://x:1"}, {MaxTracks: 10, Proxy: "не адрес"}} {
		if bad.validate() == nil {
			t.Errorf("принято: %+v", bad)
		}
	}
	for n, want := range map[int]string{1: "1 трек", 3: "3 трека", 5: "5 треков", 11: "11 треков", 21: "21 трек", 112: "112 треков"} {
		if got := tracksWord(n); got != want {
			t.Errorf("%d → %q", n, got)
		}
	}
	if (track{Title: "Forss - Flickermood", User: user{Username: "forss"}}).Name() != "Forss - Flickermood" {
		t.Error("исполнитель в названии повторён")
	}
}

// Живая проверка настоящего SoundCloud с этого компьютера (без колонки и декодера):
// OMD_SC_LIVE=https://soundcloud.com/forss/flickermood go test -run TestLive -v ./internal/components/soundcloud
func TestLive(t *testing.T) {
	link := os.Getenv("OMD_SC_LIVE")
	if link == "" {
		t.Skip("живая проверка: задайте OMD_SC_LIVE (ссылку на трек или плейлист SoundCloud)")
	}
	a := &api{hc: newClient(os.Getenv("OMD_SC_PROXY")), base: apiBase, site: siteBase, logf: t.Logf, fixedID: os.Getenv("OMD_SC_CLIENT_ID")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	u, err := control.ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	it, err := a.resolve(ctx, canonical(u))
	if err != nil {
		t.Fatal(err)
	}
	tk := it.Track
	if it.Kind != "track" {
		list, err := a.fillTracks(ctx, it.Tracks)
		if err != nil || len(list) == 0 {
			t.Fatalf("%s: треков %d, %v", it.Kind, len(list), err)
		}
		t.Logf("%s «%s»: треков %d (в ответе resolve %d, остальные догружены по номерам)", it.Kind, it.Title, len(list), len(it.Tracks))
		tk = list[0]
	}
	tc, err := tk.pick()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("трек %d: %s, поток %s (%s)", tk.ID, tk.Name(), tc.Preset, tc.Format.MimeType)
	su, err := a.streamURL(ctx, tk)
	if err != nil {
		t.Fatal(err)
	}
	st, err := hls.Open(ctx, su, hls.Options{Client: a.hc})
	if err != nil {
		t.Fatal(err)
	}
	var ex hls.Extractor
	total := 0
	for i := 0; i < 2; i++ {
		seg, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		es, err := ex.Push(seg)
		if err != nil {
			t.Fatal(err)
		}
		total += len(es)
	}
	if total == 0 || ex.Codec() != hls.CodecAAC {
		t.Fatalf("звука %d байт, кодек %v", total, ex.Codec())
	}
	t.Logf("два сегмента: %d байт кадров ADTS", total)
}
