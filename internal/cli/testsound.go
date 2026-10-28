package cli

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/config"
	"openmediad/internal/sink/alsa"
	"openmediad/internal/sink/speaker"
	"openmediad/internal/system/bluez"
)

var testSoundCmd = Command{
	Name:    "test-sound",
	Summary: "тихий тестовый сигнал в выход (проверка звука без Алисы)",
	Usage:   "[-db -30] [-sec 1] [speaker|bluetooth]",
	Help: `Играет короткий тон 440 Гц в выход мимо демона, тем же путём, что движок: системный aplay. Громкость сигнала — -db
относительно полной (по умолчанию -30 дБ, тихо); итоговую громкость ещё задаёт громкость колонки или наушников.

  speaker     динамики колонки (общий смеситель), по умолчанию
  bluetooth   подключённые наушники (PCM BlueALSA); подключить — openmediad bt pair / bt connect. Пока демон с выходом
              bluetooth работает, наушники заняты им (один клиент на PCM) — сигнал не пройдёт`,
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
		switch sinkName {
		case "speaker":
			sink := &speaker.Sink{Aplay: cfg.Speaker.Aplay, Device: cfg.Speaker.Device}
			out, err = sink.Open(f)
		case "bluetooth":
			out, err = openHeadphones(cfg.Speaker.Aplay, f)
		default:
			return UsageError{Msg: fmt.Sprintf("выход %q: есть speaker и bluetooth", sinkName)}
		}
		if err != nil {
			return err
		}
		pcm := tone(f, 440, time.Duration(*sec*float64(time.Second)), audio.DB(*db))
		c.printf("тон 440 Гц, %g с, %g дБ → %s\n", *sec, *db, sinkName)
		if _, err := out.Write(pcm); err != nil {
			out.Abort()
			if sinkName == "bluetooth" {
				// PCM BlueALSA принимает одного клиента, а демон держит канал в наушники, пока они подключены
				return fmt.Errorf("запись в aplay: %w (наушники, видимо, заняты демоном — проверьте звуком Алисы или после openmediad stop)", err)
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

// openHeadphones — aplay в подключённые наушники (первые, у которых есть PCM вывода BlueALSA).
func openHeadphones(aplay string, f audio.Format) (audio.Output, error) {
	b, err := bluez.Open()
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNotDevice, err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	pcm, ok := st.PlaybackPCM("")
	if !ok {
		return nil, errors.New("наушники не подключены (openmediad bt list)")
	}
	p, err := alsa.Start(alsa.Config{Path: aplay, Device: bluez.ALSADevice(pcm.Address), Rate: f.Rate, Channels: f.Channels})
	if err != nil {
		return nil, err
	}
	return alsa.NewOutput(p), nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
