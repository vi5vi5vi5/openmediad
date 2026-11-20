package yandexmusic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"openmediad/internal/core/control"
)

var _ control.LinkPlayer = (*Backend)(nil)

func TestParseYandexLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://music.yandex.ru/album/5307396/track/44683635?utm_source=desktop":   "track /tracks/44683635",
		"https://music.yandex.ru/track/44683635":                                    "track /tracks/44683635",
		"https://music.yandex.com/album/5307396":                                    "album /albums/5307396",
		"https://music.yandex.ru/artist/4712549/tracks":                             "artist /artists/4712549/brief-info",
		"https://music.yandex.ru/users/yamusic-daily/playlists/12345":               "playlist /users/yamusic-daily/playlists/12345",
		"https://music.yandex.ru/playlists/lk.0d6f5a8b-1c2d-4e5f-8a9b-0c1d2e3f4a5b": "playlist /playlist/lk.0d6f5a8b-1c2d-4e5f-8a9b-0c1d2e3f4a5b",
		"https://music.yandex.ru/home":                                              "",
		"https://music.yandex.ru/album/abc":                                         "",
	} {
		u, _ := url.Parse(in)
		it, err := parseYandexLink(u)
		got := ""
		if err == nil {
			got = it.kind + " " + it.api
		}
		if got != want {
			t.Errorf("%s → %q, %v; ждали %q", in, got, err, want)
		}
	}
}

// Ссылка превращается в просьбу Алисе с названием из API; слова «в яндекс музыке» уводят её от Spotify.
func TestYandexPlayLink(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/44683635":
			io.WriteString(w, `{"result":[{"id":"44683635","title":"Signs","artists":[{"name":"Ikon"}]}]}`)
		case "/albums/1":
			io.WriteString(w, `{"result":{"id":1,"title":"Quiet As It's Kept","artists":[{"name":"A"},{"name":"B"},{"name":"C"}]}}`)
		case "/artists/7/brief-info":
			io.WriteString(w, `{"result":{"artist":{"id":7,"name":"Кино"}}}`)
		case "/users/u/playlists/3":
			io.WriteString(w, `{"result":{"title":"Вечер","kind":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	var asked []string
	b := &Backend{Client: api.Client(), LinkAPI: api.URL, Ask: func(_ context.Context, text string) error { asked = append(asked, text); return nil }}
	cases := []struct{ link, title, phrase string }{
		{"https://music.yandex.ru/album/5307396/track/44683635", "Ikon — Signs", "включи Ikon Signs в яндекс музыке"},
		{"https://music.yandex.ru/album/1", "альбом A, B — Quiet As It's Kept", "включи альбом Quiet As It's Kept A, B в яндекс музыке"},
		{"https://music.yandex.ru/artist/7", "Кино", "включи Кино в яндекс музыке"},
		{"https://music.yandex.ru/users/u/playlists/3", "плейлист Вечер", "включи плейлист Вечер в яндекс музыке"},
	}
	for i, c := range cases {
		u, _ := url.Parse(c.link)
		title, err := b.PlayLink(context.Background(), u)
		if err != nil || title != c.title || len(asked) != i+1 || asked[i] != c.phrase {
			t.Fatalf("%s: %q, %v; Алисе: %q", c.link, title, err, asked)
		}
	}
	u, _ := url.Parse("https://music.yandex.ru/track/1")
	if _, err := b.PlayLink(context.Background(), u); err == nil || !strings.Contains(err.Error(), "нет") {
		t.Fatalf("несуществующий трек: %v", err)
	}
	b.Ask = nil
	if _, err := b.PlayLink(context.Background(), u); err == nil {
		t.Fatal("без связи с Алисой принято")
	}
}

// Живая проверка открытого API: OMD_YANDEX_LIVE=1 go test -run TestYandexLinkLive -v ./internal/components/yandex-music
func TestYandexLinkLive(t *testing.T) {
	if os.Getenv("OMD_YANDEX_LIVE") == "" {
		t.Skip("живая проверка: задайте OMD_YANDEX_LIVE=1")
	}
	b := &Backend{Client: http.DefaultClient}
	for _, link := range []string{"https://music.yandex.ru/album/5307396/track/44683635", "https://music.yandex.ru/album/1", "https://music.yandex.ru/artist/4712549"} {
		u, _ := url.Parse(link)
		it, _ := parseYandexLink(u)
		info, err := b.lookupYandex(context.Background(), it)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		phrase, title, err := yandexPhrase(it.kind, info)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		t.Logf("%s → %q, Алисе: %q", it.kind, title, phrase)
	}
}
