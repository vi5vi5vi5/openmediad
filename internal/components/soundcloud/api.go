package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Внутренний API сайта SoundCloud (api-v2.soundcloud.com) — тот же, которым пользуется веб-плеер. Ему нужен client_id веб-плеера: он
// публичный (лежит в скриптах сайта), но время от времени меняется. Поэтому client_id берётся из настроек, а если там пусто — находится
// на сайте сам (discover) и запоминается в файле состояния; перестал подходить (401/403) — находится заново.
//
// Ссылка → resolve → трек, плейлист (альбом) или исполнитель. У плейлиста целиком описаны только первые треки, остальные — одними номерами:
// их описания догружаются (/tracks?ids=…). Звук трека — HLS (AAC в fMP4): адрес плейлиста выдаёт отдельный запрос по ссылке из
// media.transcodings, он действует около двух часов.

const (
	apiBase  = "https://api-v2.soundcloud.com"
	siteBase = "https://soundcloud.com"
	// assetsBase — откуда страница сайта подключает свои скрипты (в одном из них client_id)
	assetsBase = "https://a-v2.sndcdn.com/assets/"
	userAgent  = "Mozilla/5.0 (X11; Linux armv7l) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	maxBody    = 8 << 20
	idsChunk   = 50 // столько треков отдаёт /tracks?ids= за раз
)

// track — трек SoundCloud (только нужные поля).
type track struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	DurationMs  int64  `json:"duration"`
	FullMs      int64  `json:"full_duration"`
	Policy      string `json:"policy"` // ALLOW, MONETIZE, SNIP (только отрывок без Go+), BLOCK (недоступен в стране)
	Streamable  *bool  `json:"streamable"`
	Permalink   string `json:"permalink_url"`
	Auth        string `json:"track_authorization"`
	User        user   `json:"user"`
	PublisherMD *struct {
		Artist string `json:"artist"`
	} `json:"publisher_metadata"`
	Media struct {
		Transcodings []transcoding `json:"transcodings"`
	} `json:"media"`
}

type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type transcoding struct {
	URL     string `json:"url"`
	Preset  string `json:"preset"`
	Snipped bool   `json:"snipped"`
	Quality string `json:"quality"` // lq, sq, hq
	Format  struct {
		Protocol string `json:"protocol"`  // hls, progressive
		MimeType string `json:"mime_type"` // audio/mp4; codecs="mp4a.40.2" …
	} `json:"format"`
}

// full — у трека есть описание (а не только номер, как у хвоста плейлиста).
func (t track) full() bool { return t.Title != "" && len(t.Media.Transcodings) > 0 }

// Name — «исполнитель — название» для человека.
func (t track) Name() string {
	artist := t.User.Username
	if t.PublisherMD != nil && t.PublisherMD.Artist != "" {
		artist = t.PublisherMD.Artist
	}
	if artist == "" || strings.Contains(strings.ToLower(t.Title), strings.ToLower(artist)) {
		return t.Title
	}
	return artist + " — " + t.Title
}

// item — то, на что ведёт ссылка: трек, плейлист (альбом) или исполнитель.
type item struct {
	Kind    string // track, playlist, system-playlist, user
	Track   track  // для track
	Title   string // плейлиста
	IsAlbum bool
	Tracks  []track // плейлиста (хвост — одними номерами)
	User    user    // автор плейлиста или сам исполнитель
}

// apiError — сервер ответил не 200.
type apiError struct{ status int }

func (e apiError) Error() string {
	switch e.status {
	case http.StatusNotFound:
		return "SoundCloud: такого нет (удалено, закрыто или опечатка в ссылке)"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "SoundCloud не пускает: client_id не подходит"
	case http.StatusTooManyRequests:
		return "SoundCloud: слишком много запросов, попробуйте позже"
	}
	return fmt.Sprintf("SoundCloud ответил %d", e.status)
}

// api — клиент внутреннего API.
type api struct {
	hc        *http.Client
	base      string // apiBase; тесты подменяют
	site      string // siteBase
	assets    string // assetsBase; тесты подменяют
	fixedID   string // client_id из настроек; пусто — искать самому
	statePath string // где помнить найденный client_id
	logf      func(string, ...any)

	cmu sync.Mutex // hc и fixedID: меняются на ходу (Reload)

	mu    sync.Mutex
	id    string // найденный client_id
	found time.Time
}

// set меняет HTTP-клиент (прокси) и client_id из настроек на ходу (Reload).
func (a *api) set(hc *http.Client, fixedID string) {
	a.cmu.Lock()
	a.hc, a.fixedID = hc, fixedID
	a.cmu.Unlock()
}

func (a *api) client() *http.Client {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return a.hc
}

func (a *api) fixed() string {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return a.fixedID
}

type savedID struct {
	ClientID string    `json:"client_id"`
	Found    time.Time `json:"found"`
}

// clientID — client_id для запросов: из настроек, из памяти, из файла или найденный на сайте.
func (a *api) clientID(ctx context.Context, refresh bool) (string, error) {
	if id := a.fixed(); id != "" {
		return id, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !refresh && a.id != "" {
		return a.id, nil
	}
	if !refresh && a.statePath != "" {
		var s savedID
		if b, err := os.ReadFile(a.statePath); err == nil && json.Unmarshal(b, &s) == nil && s.ClientID != "" {
			a.id, a.found = s.ClientID, s.Found
			return a.id, nil
		}
	}
	if refresh && time.Since(a.found) < time.Minute {
		return a.id, nil // только что нашли: значит, дело не в client_id
	}
	id, err := a.discover(ctx)
	if err != nil {
		return "", err
	}
	a.id, a.found = id, time.Now()
	if a.statePath != "" {
		b, _ := json.Marshal(savedID{id, a.found})
		os.MkdirAll(filepath.Dir(a.statePath), 0o755)
		if err := os.WriteFile(a.statePath+".new", b, 0o600); err == nil {
			os.Rename(a.statePath+".new", a.statePath)
		}
	}
	a.logf("client_id веб-плеера SoundCloud найден на сайте")
	return id, nil
}

var reClientID = regexp.MustCompile(`client_id\s*[:=]\s*"([0-9A-Za-z]{20,40})"`)

// discover находит client_id веб-плеера: он записан в одном из скриптов, которые подключает главная страница (обычно в последних).
// Качаются только скрипты с CDN сайта (assets), а не любые ссылки со страницы.
func (a *api) discover(ctx context.Context) (string, error) {
	page, err := a.fetch(ctx, a.site+"/")
	if err != nil {
		return "", fmt.Errorf("client_id SoundCloud: страница сайта не открылась: %w", err)
	}
	if m := reClientID.FindSubmatch(page); m != nil {
		return string(m[1]), nil
	}
	assets := a.assets
	if assets == "" {
		assets = assetsBase
	}
	reScript := regexp.MustCompile(`<script[^>]+src="(` + regexp.QuoteMeta(assets) + `[^"]+\.js)"`)
	scripts := reScript.FindAllSubmatch(page, -1)
	for i := len(scripts) - 1; i >= 0; i-- {
		js, err := a.fetch(ctx, string(scripts[i][1]))
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		if m := reClientID.FindSubmatch(js); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errors.New("client_id SoundCloud на сайте не найден (сайт изменился?): задайте его вручную, ключ client_id в [soundcloud]")
}

// fetch скачивает страницу или скрипт (без client_id).
func (a *api) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, plainErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError{resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// plainErr убирает ссылку из текста сетевой ошибки: в ней client_id и подписи.
func plainErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// get — запрос к API с client_id; out — куда разобрать JSON. Ответ 401/403 при найденном (не заданном) client_id — найти заново и повторить.
func (a *api) get(ctx context.Context, rawurl string, q url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		id, err := a.clientID(ctx, attempt > 0)
		if err != nil {
			return err
		}
		u, err := url.Parse(rawurl)
		if err != nil {
			return err
		}
		qq := u.Query()
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("client_id", id)
		u.RawQuery = qq.Encode()
		body, err := a.fetch(ctx, u.String())
		var ae apiError
		if errors.As(err, &ae) && (ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden) && a.fixed() == "" && attempt == 0 {
			a.logf("client_id SoundCloud перестал подходить (%d): ищу новый", ae.status)
			continue
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("SoundCloud: ответ не разобран: %w", err)
		}
		return nil
	}
}

// resolve узнаёт, на что ведёт ссылка soundcloud.com.
func (a *api) resolve(ctx context.Context, link string) (item, error) {
	var raw json.RawMessage
	if err := a.get(ctx, a.base+"/resolve", url.Values{"url": {link}}, &raw); err != nil {
		return item{}, err
	}
	var head struct {
		Kind     string  `json:"kind"`
		ID       int64   `json:"id"`
		Username string  `json:"username"`
		Title    string  `json:"title"`
		IsAlbum  bool    `json:"is_album"`
		Tracks   []track `json:"tracks"`
		User     user    `json:"user"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return item{}, fmt.Errorf("SoundCloud: ответ не разобран: %w", err)
	}
	it := item{Kind: head.Kind, Title: head.Title, IsAlbum: head.IsAlbum, Tracks: head.Tracks, User: head.User}
	switch head.Kind {
	case "track":
		if err := json.Unmarshal(raw, &it.Track); err != nil {
			return item{}, fmt.Errorf("SoundCloud: ответ не разобран: %w", err)
		}
	case "user":
		it.User = user{ID: head.ID, Username: head.Username}
	}
	return it, nil
}

// fillTracks заменяет треки-номера полными описаниями, сохраняя порядок.
func (a *api) fillTracks(ctx context.Context, list []track) ([]track, error) {
	var missing []int64
	for _, t := range list {
		if !t.full() {
			missing = append(missing, t.ID)
		}
	}
	if len(missing) == 0 {
		return list, nil
	}
	got := map[int64]track{}
	for len(missing) > 0 {
		n := min(len(missing), idsChunk)
		parts := make([]string, n)
		for i, id := range missing[:n] {
			parts[i] = strconv.FormatInt(id, 10)
		}
		var page []track
		if err := a.get(ctx, a.base+"/tracks", url.Values{"ids": {strings.Join(parts, ",")}}, &page); err != nil {
			return nil, err
		}
		for _, t := range page {
			got[t.ID] = t
		}
		missing = missing[n:]
	}
	out := list[:0:0]
	for _, t := range list {
		if !t.full() {
			var ok bool
			if t, ok = got[t.ID]; !ok {
				continue
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// userTracks — треки исполнителя (как на его странице: новые сначала).
func (a *api) userTracks(ctx context.Context, id int64, limit int) ([]track, error) {
	var page struct {
		Collection []track `json:"collection"`
	}
	err := a.get(ctx, fmt.Sprintf("%s/users/%d/tracks", a.base, id), url.Values{"limit": {strconv.Itoa(limit)}, "linked_partitioning": {"1"}}, &page)
	return page.Collection, err
}

// playable — можно ли сыграть трек целиком; если нет — почему.
func (t track) playable() error {
	switch {
	case t.Policy == "BLOCK":
		return errors.New("недоступен в этой стране")
	case t.Streamable != nil && !*t.Streamable:
		return errors.New("автор запретил прослушивание")
	}
	if _, err := t.pick(); err != nil {
		return err
	}
	return nil
}

// pick выбирает поток: HLS с AAC целиком (не отрывок), лучшее качество. Отрывок (snipped) — трек только для подписчиков Go+.
func (t track) pick() (transcoding, error) {
	var best transcoding
	score := -1
	snipped := false
	for _, tc := range t.Media.Transcodings {
		if tc.Format.Protocol != "hls" || !strings.HasPrefix(tc.Format.MimeType, "audio/mp4") || strings.Contains(tc.Preset, "abr") {
			continue
		}
		if tc.Snipped {
			snipped = true
			continue
		}
		s := map[string]int{"lq": 1, "sq": 2, "hq": 3}[tc.Quality]*1000 + presetRate(tc.Preset)
		if s > score {
			best, score = tc, s
		}
	}
	switch {
	case score >= 0:
		return best, nil
	case snipped:
		return transcoding{}, errors.New("целиком только для подписчиков SoundCloud Go+")
	}
	return transcoding{}, errors.New("у трека нет потока AAC (HLS)")
}

// presetRate — битрейт из имени пресета (aac_160k → 160).
func presetRate(p string) int {
	m := regexp.MustCompile(`(\d+)k`).FindStringSubmatch(p)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// streamURL — адрес плейлиста HLS трека (действует около двух часов).
func (a *api) streamURL(ctx context.Context, t track) (string, error) {
	tc, err := t.pick()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	if t.Auth != "" {
		q.Set("track_authorization", t.Auth)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := a.get(ctx, tc.URL, q, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("SoundCloud не дал адрес потока")
	}
	return out.URL, nil
}

// expand раскрывает короткую ссылку (on.soundcloud.com/…): перенаправления ведут на soundcloud.com.
func (a *api) expand(ctx context.Context, u *url.URL) (*url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("короткая ссылка SoundCloud не открылась: %w", plainErr(err))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("короткая ссылка SoundCloud: %w", apiError{resp.StatusCode})
	}
	return resp.Request.URL, nil
}
