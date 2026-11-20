package control

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type fakeLinks struct {
	hosts []string
	got   []string
	err   error
}

func (f *fakeLinks) LinkHosts() []string { return f.hosts }
func (f *fakeLinks) PlayLink(_ context.Context, u *url.URL) (string, error) {
	f.got = append(f.got, u.String())
	return "трек", f.err
}

func TestParseLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://soundcloud.com/forss/flickermood":   "https://soundcloud.com/forss/flickermood",
		"'https://soundcloud.com/forss/flickermood'": "https://soundcloud.com/forss/flickermood",
		"«https://x.ru/a»":                           "", // ёлочки — не кавычки ссылки: в ссылке пробелов нет, но и схема не та
		"<https://open.spotify.com/track/abc?si=1>":  "https://open.spotify.com/track/abc?si=1",
		" \"music.yandex.ru/album/1/track/2\" ":      "https://music.yandex.ru/album/1/track/2",
		"soundcloud.com/forss":                       "https://soundcloud.com/forss",
		"http://on.soundcloud.com/AbC":               "http://on.soundcloud.com/AbC",
		"кино":                                       "",
		"группа крови":                               "",
		"ftp://x.ru/a":                               "",
		"https://localhost/a":                        "",
		"https://soundcloud.com/a b":                 "",
		"v1.2":                                       "https://v1.2", // похоже на домен: решит плеер (такого домена ни у кого нет)
	} {
		u, err := ParseLink(in)
		switch {
		case want == "" && err == nil:
			t.Errorf("%q: принято как ссылка %s", in, u)
		case want != "" && (err != nil || u.String() != want):
			t.Errorf("%q: %v, %v; ждали %s", in, u, err, want)
		}
	}
}

func TestPlayLink(t *testing.T) {
	var logs []string
	h := &Hub{Logf: func(f string, a ...any) { logs = append(logs, f) }}
	sc := &fakeLinks{hosts: []string{"SoundCloud.com", "www.snd.sc"}}
	sp := &fakeLinks{hosts: []string{"open.spotify.com"}}
	short := &fakeLinks{hosts: []string{"on.soundcloud.com"}} // вложенный домен у другого плеера: у длинного приоритет
	h.AddLink("soundcloud", sc)
	h.AddLink("spotify", sp)
	h.AddLink("shortener", short)

	ctx := context.Background()
	if p, title, err := h.PlayLink(ctx, "https://m.soundcloud.com/forss/flickermood?si=secret"); p != "soundcloud" || title != "трек" || err != nil {
		t.Fatalf("soundcloud: %s %q %v", p, title, err)
	}
	if p, _, _ := h.PlayLink(ctx, "snd.sc/x"); p != "soundcloud" {
		t.Fatalf("snd.sc (без www у ссылки): %s", p)
	}
	if p, _, _ := h.PlayLink(ctx, "https://on.soundcloud.com/AbC"); p != "shortener" {
		t.Fatalf("вложенный домен: %s", p)
	}
	if p, _, _ := h.PlayLink(ctx, "https://open.spotify.com/intl-de/track/1"); p != "spotify" || len(sp.got) != 1 {
		t.Fatalf("spotify: %s", p)
	}
	if _, _, err := h.PlayLink(ctx, "https://notsoundcloud.com/x"); err == nil {
		t.Fatal("чужой домен с похожим хвостом принят")
	}
	var nl *NoLinkPlayerError
	if _, _, err := h.PlayLink(ctx, "https://youtube.com/watch?v=1"); !errors.As(err, &nl) || nl.Host != "youtube.com" ||
		strings.Join(nl.Known, ",") != "soundcloud.com,snd.sc,open.spotify.com,on.soundcloud.com" {
		t.Fatalf("ничей домен: %v", err)
	}
	if _, _, err := h.PlayLink(ctx, "включи кино"); !errors.Is(err, ErrNotLink) {
		t.Fatalf("не ссылка: %v", err)
	}
	sp.err = errors.New("Spotify не готов")
	if p, _, err := h.PlayLink(ctx, "open.spotify.com/album/2"); p != "spotify" || err == nil || err.Error() != "Spotify не готов" {
		t.Fatalf("ошибка плеера: %s %v", p, err)
	}
	for _, l := range logs {
		if strings.Contains(l, "secret") {
			t.Fatalf("ссылка попала в журнал: %s", l)
		}
	}
	if (&Hub{}).LinkHosts() != nil || (*Hub)(nil).LinkHosts() != nil {
		t.Fatal("домены пустого ядра")
	}
	if err := (&NoLinkPlayerError{Host: "x.ru"}).Error(); !strings.Contains(err, "нет плееров") {
		t.Fatal(err)
	}
}
