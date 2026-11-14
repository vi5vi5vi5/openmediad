// Package proto — клиент протокола MeetUp (https://github.com/vi5vi5vi5/MeetUp): разбор ссылки на конференцию, вход в комнату
// по WebSocket, чат, состояние участника и отправка звука. Спецификация — Server/docs/desktop-client-guide.md в репозитории
// MeetUp; номера разделов в комментариях — оттуда.
//
// Есть: вход без аккаунта и под аккаунтом (account.go), пароль комнаты, сквозное шифрование звука и чата (cipher.go).
// Чего пока нет (заглушки, см. README.md компонента): приём звука и видео (двоичные кадры выбрасываются при чтении),
// картинки чата, проверка самоподписанного сертификата по отпечатку.
package proto

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Link — разобранная ссылка на конференцию.
type Link struct {
	Secure bool   // https (WebSocket — wss://хост/ws) или http (локальный сервер без прокси: ws://хост:9000)
	Host   string // хост[:порт] в ASCII (домен с национальными буквами — в виде xn--…)
	Room   string // код комнаты
	// Key — ключ сквозного шифрования из части ссылки после «#k=» (base64url; разбирает KeyFromLink); "" — ключа в ссылке нет.
	Key string
}

var keyRe = regexp.MustCompile(`[#&]k=([A-Za-z0-9_-]+)`)

// ParseLink разбирает ссылку вида https://хост/conference.html?room=КОД[#k=КЛЮЧ]. Схему можно не писать (тогда https).
func ParseLink(s string) (Link, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Link{}, errors.New("пустая ссылка")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return Link{}, errors.New("ссылка не разобрана: проверьте, что она скопирована целиком")
	}
	var l Link
	switch u.Scheme {
	case "https":
		l.Secure = true
	case "http":
	default:
		return Link{}, fmt.Errorf("схема %q не подходит: нужна ссылка https:// или http://", u.Scheme)
	}
	host, err := asciiHost(u.Hostname())
	if err != nil {
		return Link{}, err
	}
	l.Host = host
	if p := u.Port(); p != "" {
		l.Host = net.JoinHostPort(host, p)
	}
	l.Room = strings.TrimSpace(u.Query().Get("room"))
	if l.Room == "" {
		return Link{}, errors.New("в ссылке нет кода комнаты: нужна ссылка вида https://сервер/conference.html?room=КОД")
	}
	if len(l.Room) > 128 || strings.ContainsAny(l.Room, " \t\r\n") {
		return Link{}, errors.New("код комнаты в ссылке не похож на настоящий")
	}
	if m := keyRe.FindStringSubmatch("#" + u.Fragment); m != nil {
		l.Key = m[1]
	}
	return l, nil
}

// WSURL — адрес WebSocket сервера (§2.1): за прокси — wss://хост/ws, без прокси — ws://хост:9000.
func (l Link) WSURL() string {
	if l.Secure {
		return "wss://" + l.Host + "/ws"
	}
	host := l.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "ws://" + net.JoinHostPort(host, "9000")
}

// Display — ссылка для показа человеку и для журнала: без кода комнаты целиком (код — это и есть секрет комнаты) и без ключа.
func (l Link) Display() string {
	room := l.Room
	if len(room) > 6 {
		room = room[:3] + "…" + room[len(room)-2:]
	}
	return l.Host + " · комната " + room
}

// asciiHost приводит имя хоста к виду для DNS и TLS: строчные буквы, метки с национальными буквами — в Punycode (xn--…).
// Это упрощённый IDNA: без нормализации Юникода, для обычных доменов (мит-ап.рф) этого достаточно.
func asciiHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return "", errors.New("в ссылке нет адреса сервера")
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if label == "" {
			return "", fmt.Errorf("адрес сервера %q записан с ошибкой", host)
		}
		if isASCII(label) {
			continue
		}
		labels[i] = "xn--" + punycode(label)
		if len(labels[i]) > 63 {
			return "", fmt.Errorf("слишком длинная часть адреса сервера: %q", label)
		}
	}
	return strings.Join(labels, "."), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// punycode кодирует метку домена (RFC 3492, параметры для IDNA).
func punycode(label string) string {
	const (
		base, tmin, tmax = 36, 1, 26
		skew, damp       = 38, 700
		initialBias      = 72
		initialN         = 128
	)
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, points int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / points
		k := 0
		for delta > (base-tmin)*tmax/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	runes := []rune(label)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	basic := len(out)
	done := basic
	if basic > 0 {
		out = append(out, '-')
	}
	n, delta, bias := initialN, 0, initialBias
	for done < len(runes) {
		m := int(^uint32(0) >> 1)
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		delta += (m - n) * (done + 1)
		n = m
		for _, r := range runes {
			if int(r) < n {
				delta++
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := base; ; k += base {
				t := min(max(k-bias, tmin), tmax)
				if q < t {
					break
				}
				out = append(out, digit(t+(q-t)%(base-t)))
				q = (q - t) / (base - t)
			}
			out = append(out, digit(q))
			bias = adapt(delta, done+1, done == basic)
			delta = 0
			done++
		}
		delta++
		n++
	}
	return string(out)
}
