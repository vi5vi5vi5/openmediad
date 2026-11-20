package control

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Музыка по ссылке. Человек копирует в браузере ссылку на трек, альбом или плейлист (Яндекс Музыка, Spotify, SoundCloud …) и отдаёт
// колонке: «!play https://soundcloud.com/…» в чате конференции, `openmediad play ССЫЛКА`. Кто умеет играть такие ссылки, источнику
// знать незачем: каждый плеер говорит ядру, какие домены его (LinkPlayer.LinkHosts), а Hub.PlayLink отдаёт ссылку тому, чей домен.

// LinkPlayer (необязательно для компонента) — включает музыку по ссылке своего сервиса. Им может быть и плеер движка (Spotify,
// SoundCloud), и компонент музыки платформы (Яндекс): демон подключает его по методам, как и control.Player.
type LinkPlayer interface {
	// LinkHosts — домены сервиса: "soundcloud.com", "on.soundcloud.com". Поддомены подходят сами ("m.soundcloud.com", "www.soundcloud.com");
	// у более длинного домена приоритет, если домены двух плееров вложены друг в друга.
	LinkHosts() []string
	// PlayLink включает музыку по ссылке и возвращает, что включено, для человека («исполнитель — название», «плейлист …»; пусто —
	// неизвестно). Может ходить в сеть (ctx ограничивает время). Ошибка — не включено; её текст покажут человеку как есть.
	PlayLink(ctx context.Context, link *url.URL) (title string, err error)
}

// ErrNotLink — текст не похож на ссылку.
var ErrNotLink = errors.New("это не ссылка")

// NoLinkPlayerError — ссылка на сервис, который не умеет играть ни один плеер сборки.
type NoLinkPlayerError struct {
	Host  string   // домен ссылки
	Known []string // домены, которые колонка умеет (по порядку подключения плееров)
}

func (e *NoLinkPlayerError) Error() string {
	if len(e.Known) == 0 {
		return "колонка не умеет включать музыку по ссылкам: в сборке нет плееров, которые это умеют"
	}
	return fmt.Sprintf("ссылки %s колонка не включает; умеет: %s", e.Host, strings.Join(e.Known, ", "))
}

type linkMember struct {
	name  string
	p     LinkPlayer
	hosts []string
}

// AddLink подключает плеер ссылок. Домены спрашиваются один раз, при подключении.
func (h *Hub) AddLink(name string, p LinkPlayer) {
	var hosts []string
	for _, d := range p.LinkHosts() {
		if d = normHost(d); d != "" {
			hosts = append(hosts, d)
		}
	}
	h.mu.Lock()
	h.links = append(h.links, linkMember{name, p, hosts})
	h.mu.Unlock()
}

// LinkHosts — все домены, которые умеют плееры ссылок, без повторов.
func (h *Hub) LinkHosts() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.links {
		for _, d := range m.hosts {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// ParseLink разбирает ссылку, как её вставляют в чат: с кавычками или угловыми скобками вокруг, без «https://» («soundcloud.com/…»).
// Подходят только http и https.
func ParseLink(text string) (*url.URL, error) {
	s := strings.TrimSpace(text)
	for len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if first == '<' && last == '>' || first == last && strings.ContainsRune(`'"`+"`", rune(first)) {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	if strings.ContainsAny(s, " \t\n") {
		return nil, ErrNotLink
	}
	if !strings.Contains(s, "://") {
		host, _, _ := strings.Cut(s, "/")
		if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return nil, ErrNotLink
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" || !strings.Contains(u.Hostname(), ".") {
		return nil, ErrNotLink
	}
	return u, nil
}

// LooksLikeLink — текст похож на ссылку (для источников, которые принимают и ссылку, и название: «!play кино» и «!play https://…»).
func LooksLikeLink(text string) bool {
	_, err := ParseLink(text)
	return err == nil
}

func normHost(h string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "www."), ".")
}

// linkFor — плеер, чей домен подходит к host (у более длинного домена приоритет).
func (h *Hub) linkFor(host string) (linkMember, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	h.mu.Lock()
	defer h.mu.Unlock()
	var best linkMember
	bestLen := -1
	for _, m := range h.links {
		for _, d := range m.hosts {
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > bestLen {
				best, bestLen = m, len(d)
			}
		}
	}
	return best, bestLen >= 0
}

// PlayLink включает музыку по ссылке: находит плеер по домену и отдаёт ему ссылку. Возвращает имя плеера и что включено.
// Ошибки: ErrNotLink (текст не ссылка), *NoLinkPlayerError (домен ничей), ошибка плеера (её текст — для человека).
func (h *Hub) PlayLink(ctx context.Context, text string) (player, title string, err error) {
	u, err := ParseLink(text)
	if err != nil {
		return "", "", err
	}
	m, ok := h.linkFor(u.Hostname())
	if !ok {
		return "", "", &NoLinkPlayerError{Host: u.Hostname(), Known: h.LinkHosts()}
	}
	title, err = m.p.PlayLink(ctx, u)
	if err != nil {
		h.logf("ссылка на %s: %s не включил: %v", u.Hostname(), m.name, err)
		return m.name, "", err
	}
	h.logf("ссылка на %s: включил %s", u.Hostname(), m.name) // саму ссылку в журнал не пишем: в ней бывают личные метки (?si=…, токены приглашений)
	return m.name, title, nil
}
