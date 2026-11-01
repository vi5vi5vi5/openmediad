package spotifysoloist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Поиск через Web API (GET /search). Нужен тот же вход, что и лайкам (openmediad spotify login), и, из России, прокси (web_proxy).

// searchHit — найденное: URI для Soloist (play принимает контекст: трек, альбом, плейлист, исполнитель).
type searchHit struct {
	URI     string
	Name    string
	Artists []string // у трека и альбома
	Kind    playKind
}

type searchResult struct {
	Artists, Tracks, Albums, Playlists []searchHit
}

type rawItem struct {
	URI     string `json:"uri"`
	Name    string `json:"name"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

func (w *webAPI) searchTypes(ctx context.Context, q, types string, limit int) (searchResult, error) {
	var res searchResult
	path := "/search?type=" + types + "&limit=" + strconv.Itoa(limit) + "&q=" + url.QueryEscape(q)
	st, body, err := w.apiCall(ctx, http.MethodGet, path)
	if err != nil {
		return res, err
	}
	if err := apiError(st, body); err != nil {
		return res, err
	}
	var raw struct {
		Artists   struct{ Items []*rawItem } `json:"artists"`
		Tracks    struct{ Items []*rawItem } `json:"tracks"`
		Albums    struct{ Items []*rawItem } `json:"albums"`
		Playlists struct{ Items []*rawItem } `json:"playlists"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return res, errors.New("неожиданный ответ поиска Spotify")
	}
	conv := func(items []*rawItem, kind playKind) (out []searchHit) {
		for _, it := range items {
			if it != nil && it.URI != "" && it.Name != "" { // в списках плейлистов Spotify бывают null
				h := searchHit{URI: it.URI, Name: it.Name, Kind: kind}
				for _, a := range it.Artists {
					h.Artists = append(h.Artists, a.Name)
				}
				out = append(out, h)
			}
		}
		return out
	}
	res.Artists = conv(raw.Artists.Items, kindArtist)
	res.Tracks = conv(raw.Tracks.Items, kindTrack)
	res.Albums = conv(raw.Albums.Items, kindAlbum)
	res.Playlists = conv(raw.Playlists.Items, kindPlaylist)
	return res, nil
}

// search ищет q сразу по четырём видам; limit — сколько результатов каждого вида.
func (w *webAPI) search(ctx context.Context, q string, limit int) (searchResult, error) {
	return w.searchTypes(ctx, q, "artist,track,album,playlist", limit)
}

// searchTrack ищет конкретный трек по названию и исполнителю (поля поиска Spotify: track:… artist:…).
func (w *webAPI) searchTrack(ctx context.Context, title, artist string, limit int) (searchResult, error) {
	q := `track:"` + strings.ReplaceAll(title, `"`, "") + `"`
	if artist != "" {
		q += ` artist:"` + strings.ReplaceAll(artist, `"`, "") + `"`
	}
	return w.searchTypes(ctx, q, "track", limit)
}

// minScore — ниже этого совпадение по словам считаем случайным и доверяем порядку выдачи самого Spotify.
const minScore = 0.34

// best берёт из списка кандидата с наибольшей оценкой (при равенстве — того, что выше в выдаче Spotify).
func best(q []string, hits []searchHit) (searchHit, float64, bool) {
	var top searchHit
	topScore, found := -1.0, false
	for _, h := range hits {
		if s := matchScore(q, h.Name, h.Artists...); s > topScore {
			top, topScore, found = h, s, true
		}
	}
	return top, topScore, found
}

// pick выбирает из результатов то, что просили. Для просьбы с категорией («песню», «альбом», «группу») берётся лучший кандидат этого вида.
// Для «что угодно» сравниваются все виды: «looser tame impala» — это трек Loser исполнителя Tame Impala, а не сам исполнитель, «Кино» — исполнитель.
func pick(kind playKind, query string, r searchResult) (searchHit, bool) {
	q := words(query)
	fromList := func(hs []searchHit) (searchHit, bool) {
		if len(hs) == 0 {
			return searchHit{}, false
		}
		if h, s, ok := best(q, hs); ok && s >= minScore {
			return h, true
		}
		return hs[0], true // слова не совпали (например, имя записано иначе): порядок выдачи Spotify
	}
	switch kind {
	case kindArtist:
		return fromList(r.Artists)
	case kindTrack:
		return fromList(r.Tracks)
	case kindAlbum:
		return fromList(r.Albums)
	case kindPlaylist:
		return fromList(r.Playlists)
	case kindGenre:
		if h, ok := fromList(r.Playlists); ok {
			return h, true
		}
		return fromList(r.Artists)
	}
	// «что угодно»: лучший по оценке среди всех видов; при равенстве выигрывает исполнитель, потом трек, альбом, плейлист
	var top searchHit
	topScore, found := -1.0, false
	for _, hs := range [][]searchHit{r.Artists, r.Tracks, r.Albums, r.Playlists} {
		if h, s, ok := best(q, hs); ok && s > topScore+1e-9 {
			top, topScore, found = h, s, true
		}
	}
	if found && topScore >= minScore {
		return top, true
	}
	// слова не совпали ни с чем: слово-жанр («рок», «джаз») — подборка, имя из нескольких слов, записанное иначе, — исполнитель
	if len(q) <= 1 && len(r.Playlists) > 0 {
		return r.Playlists[0], true
	}
	for _, hs := range [][]searchHit{r.Artists, r.Tracks, r.Playlists, r.Albums} {
		if len(hs) > 0 {
			return hs[0], true
		}
	}
	return searchHit{}, false
}

// findTrack ищет среди треков тот, что назвало облако: название совпало (без пояснений в скобках), первый исполнитель есть среди исполнителей.
func findTrack(tracks []searchHit, title, artist string) (searchHit, bool) {
	tw := words(cleanTitle(title))
	aw := words(firstArtist(artist))
	if len(tw) == 0 {
		return searchHit{}, false
	}
	for _, t := range tracks {
		nw := words(cleanTitle(t.Name))
		if len(nw) != len(tw) {
			continue
		}
		if c, _ := coverage(tw, nw); c != len(tw) {
			continue
		}
		if len(aw) == 0 {
			return t, true
		}
		for _, a := range t.Artists {
			if c, _ := coverage(aw, words(a)); c == len(aw) {
				return t, true
			}
		}
	}
	return searchHit{}, false
}
