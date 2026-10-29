package cli

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/config"
	"openmediad/internal/core/speaker"
)

var testSoundCmd = Command{
	Name:    "test-sound",
	Summary: "тихий тестовый сигнал в выход (проверка звука без Алисы)",
	Usage:   "[-db -30] [-sec 1] [ВЫХОД]",
	Help: `Играет короткий тон 440 Гц в выход мимо демона, тем же путём, что движок: системный aplay. Громкость сигнала — -db
относительно полной (по умолчанию -30 дБ, тихо); итоговую громкость ещё задаёт громкость колонки или наушников.

  speaker     динамики колонки (общий смеситель), по умолчанию
  bt-out      подключённые Bluetooth-наушники (PCM BlueALSA), если компонент bt-out вошёл в сборку; подключить — openmediad bt
              pair / bt connect. Пока демон с выходом bt-out работает, наушники заняты им (один клиент на PCM) — сигнал не пройдёт

Другие выходы появляются вместе с компонентами, которые их приносят (список — в сообщении об ошибке при неверном имени).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "test-sound")
		db := fs.Float64("db", -30, "уровень сигнала, дБ (0 — полная громкость, меньше — тише)")
		sec := fs.Float64("sec", 1, "длительность, с")
		if err := parse(fs, args, 1); err != nil {
			return err
		}
		sinkName := "speaker"
		if fs.NArg() == 1 {
			sinkName = fs.Arg(0)
		}
		if *db > 0 || *sec <= 0 || *sec > 10 {
			return UsageError{Msg: "-db не больше 0, -sec от 0 до 10"}
		}
		cfg, err := config.LoadFile(config.Path())
		if err != nil {
			cfg = config.Defaults()
		}
		if _, err := os.Stat(orDefault(cfg.Speaker.Aplay, "/usr/bin/aplay")); err != nil {
			return errors.New("нет aplay — тестовый сигнал играется только на колонке")
		}
		f := audio.Stereo48
		var out audio.Output
		if open, ok := command.LookupTestOutput(sinkName); ok {
			out, err = open(cfg.Speaker.Aplay, f)
		} else if sinkName == "speaker" {
			sink := &speaker.Sink{Aplay: cfg.Speaker.Aplay, Device: cfg.Speaker.Device}
			out, err = sink.Open(f)
		} else {
			names := append([]string{"speaker"}, command.TestOutputNames()...)
			return UsageError{Msg: fmt.Sprintf("выход %q: в этой сборке есть %s", sinkName, strings.Join(names, ", "))}
		}
		if err != nil {
			return err
		}
		pcm := tone(f, 440, time.Duration(*sec*float64(time.Second)), audio.DB(*db))
		c.Printf("тон 440 Гц, %g с, %g дБ → %s\n", *sec, *db, sinkName)
		if _, err := out.Write(pcm); err != nil {
			out.Abort()
			if sinkName != "speaker" {
				// выход открыт мимо демона; если демон держит его сам (наушники: PCM BlueALSA принимает одного клиента),
				// второй клиент не пройдёт
				return fmt.Errorf("запись в aplay: %w (выход, видимо, занят демоном — проверьте звуком Алисы или после openmediad stop)", err)
			}
			return fmt.Errorf("запись в aplay: %w", err)
		}
		out.Drain()
		return nil
	},
}

// tone — синус с плавными краями (20 мс), чтобы не было щелчков.
func tone(f audio.Format, hz float64, d time.Duration, gain float64) []byte {
	n := int(d.Seconds() * float64(f.Rate))
	edge := f.Rate / 50
	b := make([]byte, n*f.FrameBytes())
	for i := 0; i < n; i++ {
		env := 1.0
		if i < edge {
			env = float64(i) / float64(edge)
		} else if n-i < edge {
			env = float64(n-i) / float64(edge)
		}
		v := int16(math.Sin(2*math.Pi*hz*float64(i)/float64(f.Rate)) * env * gain * 32767)
		for ch := 0; ch < f.Channels; ch++ {
			binary.LittleEndian.PutUint16(b[(i*f.Channels+ch)*2:], uint16(v))
		}
	}
	return b
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
