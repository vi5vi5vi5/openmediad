package spotifysoloist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Музыка по ссылке (control.LinkPlayer): «Поделиться → Скопировать ссылку» в приложении или веб-плеере Spotify даёт
// https://open.spotify.com/track/<id>?si=…, ядро отдаёт её сюда, а Soloist получает её как spotify:track:<id>. Подходят трек, альбом,
// плейлист, исполнитель, выпуск и подкаст; короткие ссылки spotify.link ведут на страницу, где та же ссылка open.spotify.com.

// spotifyKinds — что бывает в ссылке и как это назвать человеку.
var spotifyKinds = map[string]string{
	"track": "трек", "album": "альбом", "playlist": "плейлист", "artist": "исполнитель", "episode": "выпуск подкаста", "show": "подкаст",
}

// shortHosts — короткие ссылки (Branch): отвечают страницей, на которой настоящая ссылка.
var shortHosts = map[string]bool{"spotify.link": true, "spotify.app.link": true}

// LinkHosts — домены ссылок Spotify.
func (c *Component) LinkHosts() []string {
	return []string{"open.spotify.com", "play.spotify.com", "spotify.link", "spotify.app.link"}
}

// spotifyURI переводит ссылку open.spotify.com в URI Spotify: /intl-de/track/<id>, /track/<id>, /user/<имя>/playlist/<id>, /embed/album/<id>.
func spotifyURI(u *url.URL) (uri, kind string, err error) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for len(parts) > 0 && (strings.HasPrefix(parts[0], "intl-") || parts[0] == "embed") {
		parts = parts[1:]
	}
	if len(parts) >= 4 && parts[0] == "user" && parts[2] == "playlist" { // старый вид ссылки на плейлист
		parts = parts[2:]
	}
	if len(parts) < 2 {
		return "", "", errors.New("в ссылке Spotify нет трека, альбома или плейлиста")
	}
	kind, id := parts[0], parts[1]
	if _, ok := spotifyKinds[kind]; !ok {
		return "", "", fmt.Errorf("ссылку на «%s» Spotify не включить: нужны трек, альбом, плейлист, исполнитель или подкаст", kind)
	}
	if !spotifyID.MatchString(id) {
		return "", "", errors.New("в ссылке Spotify негодный идентификатор")
	}
	return "spotify:" + kind + ":" + id, kind, nil
}

var (
	spotifyID   = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)
	spotifyLink = regexp.MustCompile(`https://open\.spotify\.com/(?:intl-[A-Za-z-]+/)?(?:track|album|playlist|artist|episode|show)/[0-9A-Za-z]{22}`)
)

// expandShort раскрывает короткую ссылку: идёт по перенаправлениям, а если пришла страница — ищет на ней ссылку open.spotify.com.
func (c *Component) expandShort(ctx context.Context, u *url.URL) (*url.URL, error) {
	hc := http.DefaultClient
	if c.web != nil {
		hc = c.web.client() // через прокси из настроек, если он задан
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux armv7l) openmediad")
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // без ссылки в тексте
		}
		return nil, fmt.Errorf("короткая ссылка Spotify не открылась: %w", err)
	}
	defer resp.Body.Close()
	if final := resp.Request.URL; final.Hostname() == "open.spotify.com" {
		return final, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if m := spotifyLink.Find(body); m != nil {
		return url.Parse(string(m))
	}
	return nil, errors.New("короткая ссылка Spotify никуда не ведёт")
}

// PlayLink включает в Spotify то, на что ведёт ссылка. Ждёт (недолго), пока Soloist сообщит название, чтобы ответить, что включено.
func (c *Component) PlayLink(ctx context.Context, u *url.URL) (string, error) {
	if shortHosts[strings.ToLower(u.Hostname())] {
		var err error
		if u, err = c.expandShort(ctx, u); err != nil {
			return "", err
		}
	}
	uri, kind, err := spotifyURI(u)
	if err != nil {
		return "", err
	}
	if c.ctl == nil {
		return "", errors.New("Spotify не запущен")
	}
	before := c.ctl.state()
	if !before.Connected || !before.LoggedIn {
		return "", errors.New("Spotify не готов: Soloist не подключён или не вошёл в аккаунт")
	}
	if err := c.ctl.PlayURI(uri); err != nil {
		return "", fmt.Errorf("Soloist не принял команду: %w", err)
	}
	c.logf("включено по ссылке: %s", spotifyKinds[kind])
	what := spotifyKinds[kind] + " Spotify"
	// название трека придёт от Soloist через секунду-другую; дольше не ждём — музыка уже включена
	wait, cancel := context.WithTimeout(ctx, linkTitleWait)
	defer cancel()
	for {
		ps := c.ctl.state()
		if ps.URI != "" && ps.URI != before.URI && ps.Title != "" {
			title := ps.Title
			if ps.Artist != "" {
				title = ps.Artist + " — " + title
			}
			if kind == "track" {
				return title, nil
			}
			return what + ": " + title, nil
		}
		select {
		case <-wait.Done():
			return what, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// linkTitleWait — сколько ждать название включённого по ссылке трека.
const linkTitleWait = 4 * time.Second
