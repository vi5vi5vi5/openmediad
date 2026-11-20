package yandexmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Музыка по ссылке (control.LinkPlayer): «Поделиться → Скопировать ссылку» в Яндекс Музыке даёт https://music.yandex.ru/album/<id>/track/<id>.
//
// Сыграть трек Яндекса по номеру движок сам не может: ссылку на звук и ключ к нему платформа получает от облака, и только на просьбу
// Алисе. Поэтому ссылка превращается в просьбу: название узнаётся из открытого API Яндекс Музыки (без входа), и Алисе говорится
// «включи <исполнитель> <название> в Яндекс Музыке» — как если бы это сказали голосом. Слова «Яндекс Музыка» нужны, чтобы просьбу не
// забрал Spotify («включи <название>» он ищет у себя). Поэтому включится то, что найдёт Алиса: обычно ровно этот трек, но не наверняка
// (одинаковые названия, чужой плейлист, которого нет в поиске).

const yandexAPI = "https://api.music.yandex.net"

// LinkHosts — домены Яндекс Музыки.
func (b *Backend) LinkHosts() []string {
	return []string{"music.yandex.ru", "music.yandex.com", "music.yandex.by", "music.yandex.kz", "music.yandex.uz"}
}

// yandexItem — на что ведёт ссылка: путь к описанию в API и что это.
type yandexItem struct {
	kind string // track, album, artist, playlist
	api  string // путь в API
}

var (
	reTrack     = regexp.MustCompile(`^/(?:album/\d+/)?track/(\d+)/?$`)
	reAlbum     = regexp.MustCompile(`^/album/(\d+)/?$`)
	reArtist    = regexp.MustCompile(`^/artist/(\d+)(?:/[a-z]+)?/?$`)
	rePlaylist  = regexp.MustCompile(`^/users/([^/]+)/playlists/(\d+)/?$`)
	rePlaylist2 = regexp.MustCompile(`^/playlists/([0-9A-Za-z.\-]+)/?$`)
)

// parseYandexLink разбирает путь ссылки Яндекс Музыки.
func parseYandexLink(u *url.URL) (yandexItem, error) {
	p := u.EscapedPath()
	switch {
	case reTrack.MatchString(p):
		return yandexItem{"track", "/tracks/" + reTrack.FindStringSubmatch(p)[1]}, nil
	case reAlbum.MatchString(p):
		return yandexItem{"album", "/albums/" + reAlbum.FindStringSubmatch(p)[1]}, nil
	case reArtist.MatchString(p):
		return yandexItem{"artist", "/artists/" + reArtist.FindStringSubmatch(p)[1] + "/brief-info"}, nil
	case rePlaylist.MatchString(p):
		m := rePlaylist.FindStringSubmatch(p)
		return yandexItem{"playlist", "/users/" + m[1] + "/playlists/" + m[2]}, nil
	case rePlaylist2.MatchString(p):
		return yandexItem{"playlist", "/playlist/" + rePlaylist2.FindStringSubmatch(p)[1]}, nil
	}
	return yandexItem{}, errors.New("ссылка Яндекс Музыки не на трек, альбом, исполнителя или плейлист")
}

type yaArtist struct {
	Name string `json:"name"`
}

// yaInfo — общее у описаний трека, альбома, исполнителя и плейлиста в API.
type yaInfo struct {
	Title   string     `json:"title"`
	Name    string     `json:"name"`
	Artists []yaArtist `json:"artists"`
	Artist  *yaArtist  `json:"artist"` // brief-info исполнителя
}

func (i yaInfo) artists() string {
	var names []string
	for _, a := range i.Artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
		if len(names) == 2 { // двух исполнителей Алисе хватает, длинный список ей только мешает
			break
		}
	}
	return strings.Join(names, ", ")
}

// lookupYandex узнаёт название из открытого API Яндекс Музыки.
func (b *Backend) lookupYandex(ctx context.Context, it yandexItem) (yaInfo, error) {
	hc := b.Client
	if hc == nil {
		hc = NewClient()
	}
	base := b.LinkAPI
	if base == "" {
		base = yandexAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+it.api, nil)
	if err != nil {
		return yaInfo{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return yaInfo{}, fmt.Errorf("Яндекс Музыка не ответила: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return yaInfo{}, errors.New("в Яндекс Музыке такого нет (или плейлист закрыт)")
	}
	if resp.StatusCode != http.StatusOK {
		return yaInfo{}, fmt.Errorf("Яндекс Музыка ответила %d", resp.StatusCode)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return yaInfo{}, fmt.Errorf("ответ Яндекс Музыки не разобран: %w", err)
	}
	var info yaInfo
	raw := out.Result
	if len(raw) > 0 && raw[0] == '[' { // /tracks/<id> отдаёт список
		var list []yaInfo
		if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
			return yaInfo{}, errors.New("в Яндекс Музыке такого трека нет")
		}
		info = list[0]
	} else if err := json.Unmarshal(raw, &info); err != nil {
		return yaInfo{}, fmt.Errorf("ответ Яндекс Музыки не разобран: %w", err)
	}
	if info.Artist != nil {
		info.Name = info.Artist.Name
	}
	return info, nil
}

// yandexPhrase — просьба Алисе и название для человека.
func yandexPhrase(kind string, info yaInfo) (phrase, title string, err error) {
	artists := info.artists()
	switch kind {
	case "track":
		title = joinDash(artists, info.Title)
		phrase = "включи " + strings.TrimSpace(artists+" "+info.Title)
	case "album":
		title = "альбом " + joinDash(artists, info.Title)
		phrase = "включи альбом " + strings.TrimSpace(info.Title+" "+artists)
	case "artist":
		title = info.Name
		phrase = "включи " + info.Name
	case "playlist":
		title = "плейлист " + info.Title
		phrase = "включи плейлист " + info.Title
	}
	if strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(phrase, "включи"), " альбом")) == "" || title == "" {
		return "", "", errors.New("Яндекс Музыка не назвала, что это")
	}
	return phrase + " в яндекс музыке", title, nil
}

func joinDash(a, b string) string {
	if a == "" {
		return b
	}
	return a + " — " + b
}

// PlayLink просит Алису включить то, на что ведёт ссылка.
func (b *Backend) PlayLink(ctx context.Context, u *url.URL) (string, error) {
	if b.Ask == nil {
		return "", errors.New("нет связи с Алисой")
	}
	it, err := parseYandexLink(u)
	if err != nil {
		return "", err
	}
	info, err := b.lookupYandex(ctx, it)
	if err != nil {
		return "", err
	}
	phrase, title, err := yandexPhrase(it.kind, info)
	if err != nil {
		return "", err
	}
	if err := b.Ask(ctx, phrase); err != nil {
		return "", fmt.Errorf("Алиса не приняла просьбу: %w", err)
	}
	b.logf("ссылка Яндекс Музыки: попросил Алису включить (%s)", it.kind) // название не пишем: что слушают — личное
	return title, nil
}

func (b *Backend) logf(f string, a ...any) {
	if b.Logf != nil {
		b.Logf(f, a...)
	}
}
