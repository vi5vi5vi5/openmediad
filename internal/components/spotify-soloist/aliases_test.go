package spotifysoloist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSpotifyTarget(t *testing.T) {
	for in, want := range map[string]string{
		"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M":                               "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M",
		"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M?si=abc123def": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M",
		"https://open.spotify.com/intl-ru/artist/1Xyo4u8uXC1ZmMpatF05PJ":        "spotify:artist:1Xyo4u8uXC1ZmMpatF05PJ",
		"  https://open.spotify.com/album/2msN7XBgV3JCjQ7Tq3t7i9/ ":             "spotify:album:2msN7XBgV3JCjQ7Tq3t7i9",
		"https://open.spotify.com/track/2uIX8YMNjGMD7441kqyyNU?si=x&utm=y":      "spotify:track:2uIX8YMNjGMD7441kqyyNU",
	} {
		got, err := parseSpotifyTarget(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; ждали %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "кино", "https://example.com/playlist/37i9dQZF1DXcBWIGoYBM5M", "https://open.spotify.com/user/xyz", "https://open.spotify.com/show/37i9dQZF1DXcBWIGoYBM5M",
		"spotify:show:37i9dQZF1DXcBWIGoYBM5M", "javascript:alert(1)"} {
		if _, err := parseSpotifyTarget(bad); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
}

func TestAliasStoreRoundTrip(t *testing.T) {
	a := newAliasStore(newPaths(t.TempDir(), "", ""))
	if _, ok := a.lookup("кино"); ok {
		t.Fatal("в пустом хранилище что-то нашлось")
	}
	key, uri, err := a.set("  Кино! ", "https://open.spotify.com/artist/1Xyo4u8uXC1ZmMpatF05PJ?si=q")
	if err != nil || key != "кино" || uri != "spotify:artist:1Xyo4u8uXC1ZmMpatF05PJ" {
		t.Fatalf("set: %q %q %v", key, uri, err)
	}
	a.set("Ёлка", "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M")
	// поиск не зависит от регистра, знаков и «ё»
	for _, name := range []string{"кино", "Кино", "КИНО.", "Алиса кино"} {
		if got, ok := a.lookup(name); !ok || got != uri {
			t.Errorf("lookup(%q) = %q, %v", name, got, ok)
		}
	}
	if got, ok := a.lookup("елка"); !ok || got != "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("«Ёлка» не найдена по «елка»: %q %v", got, ok)
	}
	// первое подходящее из нескольких названий
	if got, ok := a.lookup("нет такого", "кино"); !ok || got != uri {
		t.Errorf("lookup из нескольких: %q %v", got, ok)
	}
	l, _ := a.list()
	if len(l) != 2 || l[0][0] != "елка" || l[1][0] != "кино" {
		t.Errorf("список: %v", l)
	}
	if ok, _ := a.remove("КИНО"); !ok {
		t.Error("remove не нашёл привязку")
	}
	if ok, _ := a.remove("кино"); ok {
		t.Error("remove нашёл уже удалённую")
	}
	if _, ok := a.lookup("кино"); ok {
		t.Error("удалённая привязка осталась")
	}
}

func TestAliasRejectsBadInput(t *testing.T) {
	a := newAliasStore(newPaths(t.TempDir(), "", ""))
	if _, _, err := a.set("", "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"); err == nil {
		t.Error("пустое название принято")
	}
	if _, _, err := a.set("алиса", "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"); err == nil {
		t.Error("название из одних служебных слов принято")
	}
	if _, _, err := a.set("кино", "не ссылка"); err == nil {
		t.Error("плохая ссылка принята")
	}
	// испорченный файл — понятная ошибка, а не паника, и просьба не ломается
	os.WriteFile(a.path, []byte("{не json"), 0o644)
	if _, ok := a.lookup("кино"); ok {
		t.Error("из испорченного файла что-то нашлось")
	}
	if _, err := a.list(); err == nil {
		t.Error("испорченный файл не замечен")
	}
	if filepath.Base(a.path) != "aliases.json" {
		t.Error("файл привязок называется иначе")
	}
}
