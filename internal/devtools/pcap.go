// Package devtools — инструменты разработчика для openmediad-dev: разбор записей трафика, сверка шима, сжатая шкала
// событий, обезличивание записей. В демон openmediad не входит.
package devtools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/observe"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
	"openmediad/internal/devtools/capture"
)

// PcapOpt — что показывать при разборе записи.
type PcapOpt struct {
	Progress bool // события прогресса и диагностики плеера
	Noisy    bool // iohub, metricad и прочие фоновые сообщения

	// VoiceDir — класть в этот каталог голосовые чанки Алисы (Ogg/Opus) файлами .ogg.
	VoiceDir string

	// Dump — после строки команды или события плеера печатать дерево полей целиком (длинные значения обрезаются до
	// DumpMax байт, по умолчанию 160).
	Dump    bool
	DumpMax int
	// DumpTypes — какие типы сообщений печатать деревом; пусто — только команды и события плеера (8 и 127).
	DumpTypes map[wire.Type]bool
}

// DecodePcap читает classic-pcap (tcpdump -w) с трафиком шины на lo и печатает кадры обоих направлений. Файл целиком
// в память не читается, поэтому подходит и для `tcpdump -w - | openmediad-dev pcap -`.
func DecodePcap(r io.Reader, o PcapOpt, out io.Writer) error {
	return capture.WalkPcap(r, func(f capture.Frame) { printFrame(f, o, out) })
}

func printFrame(f capture.Frame, o PcapOpt, out io.Writer) {
	m, err := wire.Unwrap(f.Data)
	if err != nil {
		return
	}
	if o.VoiceDir != "" && m.Type == wire.TypeAudioCommand {
		c := msg.AudioCommand(m.Body)
		if chunk, ok := c.VoiceChunk(); ok {
			name := fmt.Sprintf("%s-%s.ogg", f.Time.Format("150405.000"), observe.Short(c.Player().ID))
			if err := os.WriteFile(filepath.Join(o.VoiceDir, name), chunk, 0o600); err != nil {
				fmt.Fprintf(out, "%s voice-dir: %v\n", f.Time.Format("15:04:05.000"), err)
			}
		}
	}
	line, show := observe.Describe(f.Svc, m, observe.Opt{Progress: o.Progress, Noisy: o.Noisy})
	if !show {
		return
	}
	dir := "S→C"
	if f.ToSvc {
		dir = "C→S"
	}
	fmt.Fprintf(out, "%s %-11s %s %s\n", f.Time.Format("15:04:05.000"), f.Svc, dir, line)
	if o.Dump && dumpType(o, m.Type) {
		max := o.DumpMax
		if max <= 0 {
			max = 160
		}
		fmt.Fprintf(out, "    %s\n", pb.Dump(m.Body, max))
	}
}

// dumpType — печатать ли сообщение этого типа деревом полей (-dump).
func dumpType(o PcapOpt, t wire.Type) bool {
	if len(o.DumpTypes) > 0 {
		return o.DumpTypes[t]
	}
	return t == wire.TypeAudioCommand || t == wire.TypeAudioEvent
}

// DecodeFile — то же, что DecodePcap, для файла любого вида: pcap или обезличенный журнал кадров (.omdf).
func DecodeFile(path string, o PcapOpt, out io.Writer) error {
	return capture.WalkFile(path, func(f capture.Frame) { printFrame(f, o, out) })
}
