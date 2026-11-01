package spotifysoloist

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Свои названия. «Алиса, включи Кино» можно привязать к конкретной ссылке Spotify без всякого поиска: `openmediad spotify alias add "кино" <ссылка>`.
// Это работает и там, где Web API закрыт (поиск через него требует прокси), и точнее поиска: «вечер» — это ваш плейлист, а не первый попавшийся.
// Файл <каталог>/aliases.json: {"название": "spotify:playlist:…"}; читается при каждой просьбе, перезапуск не нужен.

type aliasStore struct{ path string }

func newAliasStore(p paths) aliasStore { return aliasStore{path: filepath.Join(p.dir, "aliases.json")} }

func (a aliasStore) load() (map[string]string, error) {
	b, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", a.path, err)
	}
	return m, nil
}

func (a aliasStore) save(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

// set привязывает название к ссылке или URI Spotify.
func (a aliasStore) set(name, target string) (key, uri string, err error) {
	key = normalize(name)
	if key == "" || len(key) > 60 {
		return "", "", errors.New("название пустое или длиннее 60 символов")
	}
	if uri, err = parseSpotifyTarget(target); err != nil {
		return "", "", err
	}
	m, err := a.load()
	if err != nil {
		return "", "", err
	}
	m[key] = uri
	return key, uri, a.save(m)
}

// remove убирает привязку; false — такой не было.
func (a aliasStore) remove(name string) (bool, error) {
	m, err := a.load()
	if err != nil {
		return false, err
	}
	key := normalize(name)
	if _, ok := m[key]; !ok {
		return false, nil
	}
	delete(m, key)
	return true, a.save(m)
}

// lookup ищет первое из названий среди привязанных.
func (a aliasStore) lookup(names ...string) (string, bool) {
	m, err := a.load()
	if err != nil || len(m) == 0 {
		return "", false
	}
	for _, n := range names {
		if uri, ok := m[normalize(n)]; ok && musicURIRe.MatchString(uri) {
			return uri, true
		}
	}
	return "", false
}

// list — привязки по алфавиту.
func (a aliasStore) list() ([][2]string, error) {
	m, err := a.load()
	if err != nil {
		return nil, err
	}
	out := make([][2]string, 0, len(m))
	for k, v := range m {
		out = append(out, [2]string{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

var openLinkRe = regexp.MustCompile(`^/(?:intl-[a-z]{2,3}/)?(playlist|album|artist|track)/([A-Za-z0-9]{10,40})/?$`)

// parseSpotifyTarget принимает URI (spotify:playlist:…) или ссылку «Поделиться → Копировать ссылку» (https://open.spotify.com/playlist/…?si=…).
func parseSpotifyTarget(s string) (string, error) {
	s = strings.TrimSpace(s)
	if musicURIRe.MatchString(s) {
		return s, nil
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host == "open.spotify.com" {
		if m := openLinkRe.FindStringSubmatch(u.Path); m != nil {
			return "spotify:" + m[1] + ":" + m[2], nil
		}
	}
	return "", errors.New("нужна ссылка вида https://open.spotify.com/playlist/… (в приложении: «Поделиться» → «Копировать ссылку») или URI spotify:playlist:… (подходят плейлист, альбом, исполнитель, трек)")
}
