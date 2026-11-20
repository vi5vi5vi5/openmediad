// Package hls — приём звукового потока HLS (RFC 8216): разбор плейлистов, выбор варианта, чтение сегментов живого потока
// по порядку и извлечение из них звука (AAC или MP3). Так вещают радиостанции, которые включает Алиса, и SoundCloud (запись, сегменты fMP4).
//
// Пакет только качает и разбирает: декодирования здесь нет (звук отдаётся сжатым, как пришёл), ссылки в журнал не пишутся
// (в них бывают метки сессии).
package hls

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Variant — вариант потока из главного плейлиста.
type Variant struct {
	URL       string
	Bandwidth int    // бит/с
	Codecs    string // как в плейлисте, например "mp4a.40.2"
}

// Segment — сегмент медиа-плейлиста.
type Segment struct {
	URL      string
	Seq      uint64 // порядковый номер (EXT-X-MEDIA-SEQUENCE + место в плейлисте)
	Duration time.Duration
	Map      string // сегмент инициализации fMP4 (EXT-X-MAP), который действует для этого сегмента; пусто — сегмент самодостаточен (MPEG-TS, голый звук)
}

// Playlist — разобранный плейлист: главный (Variants) или медиа (Segments).
type Playlist struct {
	Variants []Variant

	Segments []Segment
	Target   time.Duration // EXT-X-TARGETDURATION: наибольшая длительность сегмента
	Ended    bool          // EXT-X-ENDLIST: новых сегментов не будет
}

// Master — это главный плейлист (список вариантов).
func (p *Playlist) Master() bool { return len(p.Variants) > 0 }

// ErrUnsupported — поток устроен так, как этот пакет не умеет: шифрованные сегменты, кусок файла вместо сегмента (BYTERANGE), звук не AAC и не MP3.
var ErrUnsupported = errors.New("hls: поток не поддержан")

// Parse разбирает плейлист; base — его адрес (относительные ссылки внутри считаются от него).
func Parse(text string, base *url.URL) (*Playlist, error) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() || !strings.HasPrefix(strings.TrimPrefix(sc.Text(), "\xEF\xBB\xBF"), "#EXTM3U") {
		return nil, errors.New("hls: это не плейлист (нет #EXTM3U)")
	}
	p := &Playlist{}
	var seq uint64
	var dur time.Duration
	var variant *Variant
	var initURL string // действующий EXT-X-MAP
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := attributes(line[len("#EXT-X-STREAM-INF:"):])
			bw, _ := strconv.Atoi(attrs["BANDWIDTH"])
			variant = &Variant{Bandwidth: bw, Codecs: attrs["CODECS"]}
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseUint(line[len("#EXT-X-MEDIA-SEQUENCE:"):], 10, 64)
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if v, err := strconv.ParseFloat(line[len("#EXT-X-TARGETDURATION:"):], 64); err == nil {
				p.Target = time.Duration(v * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(line[len("#EXTINF:"):], ",")
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				dur = time.Duration(f * float64(time.Second))
			}
		case line == "#EXT-X-ENDLIST":
			p.Ended = true
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			if m := attributes(line[len("#EXT-X-KEY:"):])["METHOD"]; m != "" && m != "NONE" {
				return nil, fmt.Errorf("%w: сегменты зашифрованы (%s)", ErrUnsupported, m)
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := attributes(line[len("#EXT-X-MAP:"):])
			if attrs["BYTERANGE"] != "" {
				return nil, fmt.Errorf("%w: сегмент инициализации — кусок файла (BYTERANGE)", ErrUnsupported)
			}
			u, err := base.Parse(attrs["URI"])
			if attrs["URI"] == "" || err != nil {
				return nil, errors.New("hls: в EXT-X-MAP негодная ссылка")
			}
			initURL = u.String()
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			return nil, fmt.Errorf("%w: сегменты — куски файла (BYTERANGE)", ErrUnsupported)
		case strings.HasPrefix(line, "#"):
		default: // ссылка: вариант (после EXT-X-STREAM-INF) или сегмент
			u, err := base.Parse(line)
			if err != nil {
				return nil, errors.New("hls: в плейлисте негодная ссылка")
			}
			if variant != nil {
				variant.URL = u.String()
				p.Variants = append(p.Variants, *variant)
				variant = nil
				continue
			}
			p.Segments = append(p.Segments, Segment{URL: u.String(), Seq: seq, Duration: dur, Map: initURL})
			seq++
			dur = 0
		}
	}
	if len(p.Variants) == 0 && len(p.Segments) == 0 && !p.Ended {
		return nil, errors.New("hls: в плейлисте нет ни вариантов, ни сегментов")
	}
	return p, nil
}

// attributes разбирает список атрибутов тега (КЛЮЧ=значение через запятую; значения в кавычках могут содержать запятые).
func attributes(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			val, rest = rest[1:1+end], rest[2+end:]
			rest = strings.TrimPrefix(rest, ",")
		} else {
			val, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.TrimSpace(key)] = val
		s = rest
	}
	return out
}

// Best выбирает вариант для звука: самый большой битрейт среди вариантов без видео; если все с видео — просто самый большой.
func (p *Playlist) Best() (Variant, bool) {
	var best Variant
	found, bestAudio := false, false
	for _, v := range p.Variants {
		audio := !hasVideo(v.Codecs)
		if !found || audio && !bestAudio || audio == bestAudio && v.Bandwidth > best.Bandwidth {
			best, found, bestAudio = v, true, audio
		}
	}
	return best, found
}

func hasVideo(codecs string) bool {
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		for _, v := range []string{"avc", "hvc", "hev", "vp0", "av01", "mp4v"} {
			if strings.HasPrefix(c, v) {
				return true
			}
		}
	}
	return false
}
