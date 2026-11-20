package spotifysoloist

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	playctl "openmediad/internal/core/control"
)

var _ playctl.LinkPlayer = (*Component)(nil)

func TestSpotifyURI(t *testing.T) {
	const id = "4cOdK2wGLETKBW3PvgPWqT"
	for in, want := range map[string]string{
		"https://open.spotify.com/track/" + id + "?si=abc":         "spotify:track:" + id,
		"https://open.spotify.com/intl-de/album/" + id:             "spotify:album:" + id,
		"https://open.spotify.com/intl-pt-BR/playlist/" + id + "/": "", // регистр в intl- бывает любой: ниже — проверка, что intl- снят
		"https://open.spotify.com/user/someone/playlist/" + id:     "spotify:playlist:" + id,
		"https://open.spotify.com/embed/artist/" + id:              "spotify:artist:" + id,
		"https://open.spotify.com/episode/" + id:                   "spotify:episode:" + id,
		"https://open.spotify.com/genre/" + id:                     "",
		"https://open.spotify.com/track/short":                     "",
		"https://open.spotify.com/":                                "",
	} {
		u, _ := url.Parse(in)
		got, _, err := spotifyURI(u)
		if strings.Contains(in, "pt-BR") {
			want = "spotify:playlist:" + id
		}
		if want == "" && err == nil || want != "" && got != want {
			t.Errorf("%s → %q, %v; ждали %q", in, got, err, want)
		}
	}
}

// Ссылка уходит в Soloist командой play с URI; ответ — название, которое Soloist сообщит.
func TestPlayLink(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	u, _ := url.Parse("https://open.spotify.com/intl-ru/album/4cOdK2wGLETKBW3PvgPWqT?si=x")
	done := make(chan string, 1)
	go func() {
		title, err := r.c.PlayLink(context.Background(), u)
		if err != nil {
			title = "ошибка: " + err.Error()
		}
		done <- title
	}()
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || !strings.Contains(m, `"uri":"spotify:album:4cOdK2wGLETKBW3PvgPWqT"`) {
		t.Fatalf("в Soloist ушло: %s", m)
	}
	if title := <-done; !strings.HasPrefix(title, "альбом Spotify") {
		t.Fatalf("ответ: %q", title)
	}

	notReady := newVoiceRig(t, nil)
	if _, err := notReady.c.PlayLink(context.Background(), u); err == nil || !strings.Contains(err.Error(), "не готов") {
		t.Fatalf("без входа: %v", err)
	}
	notReady.noCommand(t)
}

// Короткая ссылка: страница со ссылкой open.spotify.com (так отвечает spotify.link) или перенаправление на неё.
func TestExpandShortLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><head><meta property="og:url" content="https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M?si=1"></head></html>`)
	}))
	defer srv.Close()
	c := &Component{}
	u, _ := url.Parse(srv.URL + "/AbCdE")
	got, err := c.expandShort(context.Background(), u)
	if err != nil || got.String() != "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" {
		t.Fatalf("раскрыто %v, %v", got, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html></html>") }))
	defer empty.Close()
	u, _ = url.Parse(empty.URL)
	if _, err := c.expandShort(context.Background(), u); err == nil {
		t.Fatal("страница без ссылки принята")
	}
}
