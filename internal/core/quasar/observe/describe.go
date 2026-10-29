// Package observe — наблюдение за шиной: клиент-наблюдатель (подключается к сервису как обычный клиент и читает его
// рассылки) и описание кадров одной строкой для людей. Нужен инструментам разработчика и журналу демона (диалог Алисы).
package observe

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
)

// Opt — что показывать. По умолчанию скрыт фон: прогресс плеера (1 Гц), iohub (спектр 20 Гц), метрики.
type Opt struct {
	Progress bool // события прогресса и диагностики плеера
	Noisy    bool // iohub, metricad и прочие фоновые сообщения
}

// Describe возвращает строку для кадра. show=false — кадр нужно только посчитать, но не печатать.
func Describe(svc string, m wire.Msg, o Opt) (line string, show bool) {
	switch m.Type {
	case wire.TypeAliceState:
		return describeAlice(msg.AliceState(m.Body)), true

	case wire.TypeAudioEvent:
		e := msg.AudioEvent(m.Body)
		if k := e.Kind(); (k == 2 || k == 5) && !o.Progress {
			return "", false
		}
		p := e.Player()
		var b strings.Builder
		fmt.Fprintf(&b, "AudioEvent %s %s %s/%s", e.Kind(), e.State(), p.Kind, Short(p.ID))
		if t := e.TrackID(); t != "" {
			b.WriteString(" track=" + t)
		}
		if pos, ok := e.PositionMs(); ok {
			fmt.Fprintf(&b, " pos=%.1fs", float64(pos)/1000)
		}
		return b.String(), true

	case wire.TypeAudioCommand:
		return describeCommand(msg.AudioCommand(m.Body)), true

	case wire.TypeRemoting:
		r := msg.Remoting(m.Body)
		s := fmt.Sprintf("Remoting %s methods=%v", r.Object(), r.Methods())
		if ev, ok := pb.Bytes(r, 18); ok {
			k, _ := pb.Uint(ev, 1)
			s += fmt.Sprintf(" event=%d", k)
		}
		return s, true

	case wire.TypeHandshake:
		h := msg.Handshake(m.Body)
		app, ver, _, model := h.Device()
		return fmt.Sprintf("Handshake app=%s ver=%s model=%s features=%d", app, ver, model, len(h.Features())), true
	}
	if !o.Noisy {
		return "", false
	}
	return fmt.Sprintf("%s len=%d", m.Type, len(m.Raw)), true
}

func describeAlice(s msg.AliceState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AliceState %s", s.Kind())
	if t := s.Text(); t != "" {
		fmt.Fprintf(&b, " text=%q", t)
	}
	if _, final, ok := s.ASR(); ok && final {
		b.WriteString(" final")
	}
	if id := s.RequestID(); id != "" {
		b.WriteString(" req=" + Short(id))
	}
	if speech, js, ok := s.Reply(); ok {
		fmt.Fprintf(&b, " reply=%q directives=%v", speech, directiveNames(js))
	}
	return b.String()
}

func describeCommand(c msg.AudioCommand) string {
	p := c.Player()
	kind := c.Classify()
	var b strings.Builder
	fmt.Fprintf(&b, "AudioCommand %s %s/%s", kind, p.Kind, Short(p.ID))
	switch kind {
	case msg.CmdVoiceChunk:
		chunk, _ := c.VoiceChunk()
		fmt.Fprintf(&b, " %dB", len(chunk))
	case msg.CmdPlay, msg.CmdPrefetch:
		if t, ok := c.Track(); ok {
			fmt.Fprintf(&b, " track=%s off=%dms", t.ID, t.OffsetMs)
			if t.Title != "" {
				fmt.Fprintf(&b, " %q", t.Artist+" — "+t.Title)
			}
			b.WriteString(" src=" + URLHost(t.URL))
		}
	}
	if r := c.Reason(); r != "" {
		fmt.Fprintf(&b, " reason=%q", r)
	}
	return b.String()
}

// directiveNames достаёт имена директив из JSON ответа облака (response.directives[].name).
func directiveNames(js []byte) []string {
	var v struct {
		Response struct {
			Directives []struct {
				Name string `json:"name"`
			} `json:"directives"`
		} `json:"response"`
	}
	if json.Unmarshal(js, &v) != nil {
		return nil
	}
	out := make([]string, 0, len(v.Response.Directives))
	for _, d := range v.Response.Directives {
		out = append(out, d.Name)
	}
	return out
}

// URLHost оставляет от ссылки только хост: в ссылках на потоки лежат подписанные токены, их не печатаем.
func URLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "?"
	}
	return u.Host
}

// Short — первые 8 символов идентификатора (uuid плеера, id запроса): для журналов хватает.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
