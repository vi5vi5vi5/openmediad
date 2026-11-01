package spotifysoloist

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const searchBody = `{
 "artists":{"items":[{"uri":"spotify:artist:AAAAAAAAAAAAAAAAAAAAAA","name":"Кино"},{"uri":"spotify:artist:BBBBBBBBBBBBBBBBBBBBBB","name":"Кино Кино"}]},
 "tracks":{"items":[{"uri":"spotify:track:CCCCCCCCCCCCCCCCCCCCCC","name":"Группа крови","artists":[{"name":"Кино"}]}]},
 "albums":{"items":[{"uri":"spotify:album:DDDDDDDDDDDDDDDDDDDDDD","name":"Группа крови","artists":[{"name":"Кино"}]}]},
 "playlists":{"items":[null,{"uri":"spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE","name":"Рок"},{"uri":"spotify:playlist:FFFFFFFFFFFFFFFFFFFFFF","name":"Лучший рок"}]}
}`

func searchRig(t *testing.T, body string) (*webAPI, *fakeSpotify) {
	t.Helper()
	f := newFakeSpotify(t)
	f.searchJSON = body
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	return w, f
}

func TestSearchParsesAllKindsAndSkipsNulls(t *testing.T) {
	w, f := searchRig(t, searchBody)
	res, err := w.search(context.Background(), "кино", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Artists) != 2 || len(res.Tracks) != 1 || len(res.Albums) != 1 || len(res.Playlists) != 2 {
		t.Fatalf("разобрано: %+v", res)
	}
	if res.Playlists[0].Name != "Рок" || res.Playlists[0].Kind != kindPlaylist {
		t.Fatalf("null в списке плейлистов не пропущен: %+v", res.Playlists)
	}
	calls := f.callLog()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "GET /search?") || !strings.Contains(calls[0], "type=artist%2Ctrack%2Calbum%2Cplaylist") && !strings.Contains(calls[0], "type=artist,track,album,playlist") {
		t.Fatalf("запрос поиска: %v", calls)
	}
	if !strings.Contains(calls[0], "q=%D0%BA%D0%B8%D0%BD%D0%BE") {
		t.Fatalf("запрос не в URL-кодировке: %v", calls)
	}
}

func TestPickByKind(t *testing.T) {
	w, _ := searchRig(t, searchBody)
	res, _ := w.search(context.Background(), "кино", 5)
	for _, c := range []struct {
		kind  playKind
		query string
		want  string
	}{
		{kindArtist, "кино", "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA"},
		{kindTrack, "группа крови", "spotify:track:CCCCCCCCCCCCCCCCCCCCCC"},
		{kindAlbum, "группа крови", "spotify:album:DDDDDDDDDDDDDDDDDDDDDD"},
		{kindPlaylist, "рок", "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE"},
		{kindGenre, "рок", "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE"},
		// «что угодно»: точное название решает, какого вида результат
		{kindAny, "Кино", "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA"},
		{kindAny, "группа крови", "spotify:track:CCCCCCCCCCCCCCCCCCCCCC"},
		{kindAny, "рок", "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE"},
	} {
		h, ok := pick(c.kind, c.query, res)
		if !ok || h.URI != c.want {
			t.Errorf("pick(%v, %q) = %+v %v; ждали %s", c.kind, c.query, h, ok, c.want)
		}
	}
}

func TestPickWithoutExactMatch(t *testing.T) {
	r := searchResult{
		Artists:   []searchHit{{URI: "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA", Name: "Billie Eilish", Kind: kindArtist}},
		Playlists: []searchHit{{URI: "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE", Name: "This Is Billie Eilish", Kind: kindPlaylist}},
	}
	// имя из нескольких слов, написанное иначе, чем в Spotify (транскрипция): берём первого исполнителя, как ранжировал Spotify
	if h, ok := pick(kindAny, "билли айлиш", r); !ok || h.Kind != kindArtist {
		t.Errorf("многословное имя: %+v %v", h, ok)
	}
	// одно слово без точного совпадения — скорее жанр или настроение: подборка
	if h, ok := pick(kindAny, "весёлое", r); !ok || h.Kind != kindPlaylist {
		t.Errorf("слово-жанр: %+v %v", h, ok)
	}
	if _, ok := pick(kindAny, "что-то", searchResult{}); ok {
		t.Error("из пустого результата что-то выбрано")
	}
	if _, ok := pick(kindAlbum, "кино", r); ok {
		t.Error("альбом выбран там, где альбомов нет")
	}
}

func TestSearchErrors(t *testing.T) {
	w, f := searchRig(t, searchBody)
	f.mu.Lock()
	f.geo = true
	f.mu.Unlock()
	if _, err := w.search(context.Background(), "кино", 5); err == nil || !strings.Contains(err.Error(), "web_proxy") {
		t.Fatalf("геоблокировка поиска: %v", err)
	}
	f.mu.Lock()
	f.geo = false
	f.searchJSON = "не json"
	f.mu.Unlock()
	if _, err := w.search(context.Background(), "кино", 5); err == nil {
		t.Fatal("разбор мусора не заметил")
	}
	f.mu.Lock()
	f.status["/v1/search"] = http.StatusTooManyRequests
	f.mu.Unlock()
	if _, err := w.search(context.Background(), "кино", 5); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("429: %v", err)
	}
}
