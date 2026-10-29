package devtools

import (
	"fmt"
	"io"
	"maps"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/observe"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
	"openmediad/internal/devtools/capture"
)

// TimelineOpt — что показывать на шкале.
type TimelineOpt struct {
	Progress bool // события прогресса (раз в секунду)
	Voice    bool // голосовой плеер Алисы
}

// Timeline печатает сжатую шкалу команд и событий плеера — то, что видела платформа. В записи со стороны шима берётся
// клиентская сторона (127.0.0.1 ↔ шим на 19906), в обычной — прямое соединение. Рассылка событий на четыре соединения
// платформы схлопывается в одну строку.
func Timeline(path string, o TimelineOpt, out io.Writer) error {
	ports := maps.Clone(capture.Ports)
	ports[19906] = "audioclient" // клиентская сторона шима
	last := map[string]int64{}
	var t0 int64
	return capture.WalkFilePorts(path, ports, func(fr capture.Frame) {
		if fr.Svc != "audioclient" || fr.ClientIP != [4]byte{127, 0, 0, 1} { // только то, что видит платформа
			return
		}
		m, err := wire.Unwrap(fr.Data)
		if err != nil {
			return
		}
		ms := fr.Time.UnixMilli()
		if t0 == 0 {
			t0 = ms
		}
		rel := float64(ms-t0) / 1000
		switch m.Type {
		case wire.TypeAudioCommand:
			c := msg.AudioCommand(m.Body)
			p := c.Player()
			k := c.Classify()
			if p.Kind == msg.PlayerVoice && !o.Voice || k == msg.CmdOther && p.ID == "" || k == msg.CmdEqualizer {
				return
			}
			if k == msg.CmdFocus {
				v, _ := pb.Uint(c, 39)
				fmt.Fprintf(out, "%9.3f КОМАНДА focus %d\n", rel, v)
				return
			}
			fmt.Fprintf(out, "%9.3f КОМАНДА %-11s %s/%s\n", rel, k, p.Kind, observe.Short(p.ID))
		case wire.TypeAudioEvent:
			e := msg.AudioEvent(m.Body)
			p := e.Player()
			if p.Kind == msg.PlayerVoice && !o.Voice || e.Kind() == 5 || (e.Kind() == 2 && !o.Progress) {
				return
			}
			pos, _ := e.PositionMs()
			key := fmt.Sprintf("%v/%v/%s/%d", e.Kind(), e.State(), p.ID, pos)
			if l, ok := last[key]; ok && ms-l < 80 {
				return
			}
			last[key] = ms
			d, _ := pb.Bytes(m.Body, 3)
			dur, _ := pb.Uint(d, 6)
			fmt.Fprintf(out, "%9.3f   событие %-8v %-8v %s/%s позиция %d мс, длительность %d с\n", rel, e.Kind(), e.State(), p.Kind, observe.Short(p.ID), pos, dur)
		}
	})
}
