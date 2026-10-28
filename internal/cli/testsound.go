package cli

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/config"
	"openmediad/internal/sink/speaker"
)

var testSoundCmd = Command{
	Name:    "test-sound",
	Summary: "тихий тестовый сигнал в выход (проверка звука без Алисы)",
	Usage:   "[-db -30] [-sec 1] [выход]",
	Help: `Играет короткий тон 440 Гц в выход (по умолчанию speaker — динамики) мимо демона, тем же путём, что движок:
системный aplay и общий смеситель колонки. Громкость сигнала — -db относительно полной (по умолчанию -30 дБ, тихо);
итоговую громкость ещё задаёт громкость колонки.`,
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
		if sinkName != "speaker" {
			return fmt.Errorf("выход %q: в этой сборке тест есть только для speaker", sinkName)
		}
		if _, err := os.Stat(orDefault(cfg.Speaker.Aplay, "/usr/bin/aplay")); err != nil {
			return errors.New("нет aplay — тестовый сигнал играется только на колонке")
		}
		sink := &speaker.Sink{Aplay: cfg.Speaker.Aplay, Device: cfg.Speaker.Device}
		f := audio.Stereo48
		out, err := sink.Open(f)
		if err != nil {
			return err
		}
		pcm := tone(f, 440, time.Duration(*sec*float64(time.Second)), audio.DB(*db))
		c.printf("тон 440 Гц, %g с, %g дБ → %s\n", *sec, *db, sinkName)
		if _, err := out.Write(pcm); err != nil {
			out.Abort()
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
