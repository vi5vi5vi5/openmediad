package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"openmediad/internal/core/platform/clock"
	"openmediad/internal/core/quasar/observe"
	"openmediad/internal/core/quasar/wire"
	"openmediad/internal/devtools"
	"openmediad/internal/libs/alsactl"
)

func observeCmd(args []string) error {
	fs := flag.NewFlagSet("observe", flag.ExitOnError)
	aliced := fs.String("aliced", "127.0.0.1:9875", "адрес aliced (пусто — не подключаться)")
	audio := fs.String("audio", "127.0.0.1:9906", "адрес audioclient (пусто — не подключаться)")
	progress := fs.Bool("progress", false, "показывать события прогресса плеера")
	noisy := fs.Bool("noisy", false, "показывать фоновые сообщения")
	stats := fs.Duration("stats", 0, "период сводки по типам кадров, например 10s")
	mono := fs.Bool("mono", false, "добавлять к времени монотонную метку (для сопоставления с pcmwatch)")
	bind := fs.String("bind", "", "локальный адрес исходящих соединений, например 127.0.0.3")
	fs.Parse(args)

	o := &observe.Observer{Opt: observe.Opt{Progress: *progress, Noisy: *noisy}, Out: os.Stdout, Stats: *stats}
	if *mono {
		o.Mono = clock.Mono
	}
	if *bind != "" {
		if o.Bind = net.ParseIP(*bind); o.Bind == nil {
			return fmt.Errorf("-bind: не адрес: %q", *bind)
		}
	}
	if *aliced != "" {
		o.Targets = append(o.Targets, observe.Target{Name: "aliced", Addr: *aliced})
	}
	if *audio != "" {
		o.Targets = append(o.Targets, observe.Target{Name: "audioclient", Addr: *audio})
	}
	if len(o.Targets) == 0 {
		return fmt.Errorf("нечего наблюдать")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := o.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func pcapCmd(args []string) error {
	fs := flag.NewFlagSet("pcap", flag.ExitOnError)
	progress := fs.Bool("progress", false, "показывать события прогресса плеера")
	noisy := fs.Bool("noisy", false, "показывать фоновые сообщения")
	voiceDir := fs.String("voice-dir", "", "сохранять голос Алисы (Ogg/Opus) файлами .ogg в этот каталог")
	dump := fs.Bool("dump", false, "после команд и событий плеера печатать дерево полей целиком")
	dumpMax := fs.Int("dump-max", 160, "-dump: обрезать значения длиннее стольких байт")
	dumpTypes := fs.String("dump-types", "", "-dump: номера типов сообщений через запятую (по умолчанию 8,127); например 85 — iohub")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return usageError("pcap")
	}
	if *voiceDir != "" {
		if err := os.MkdirAll(*voiceDir, 0o700); err != nil {
			return err
		}
	}
	o := devtools.PcapOpt{Progress: *progress, Noisy: *noisy, VoiceDir: *voiceDir, Dump: *dump, DumpMax: *dumpMax, DumpTypes: parseTypes(*dumpTypes)}
	if p := fs.Arg(0); p != "-" {
		return devtools.DecodeFile(p, o, os.Stdout)
	}
	return devtools.DecodePcap(os.Stdin, o, os.Stdout)
}

// parseTypes разбирает «8,127,85» в набор типов сообщений.
func parseTypes(s string) map[wire.Type]bool {
	out := map[wire.Type]bool{}
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out[wire.Type(n)] = true
		}
	}
	return out
}

func timelineCmd(args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	progress := fs.Bool("progress", false, "показывать события прогресса (раз в секунду)")
	voice := fs.Bool("voice", false, "показывать голосовой плеер Алисы")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return usageError("timeline")
	}
	return devtools.Timeline(fs.Arg(0), devtools.TimelineOpt{Progress: *progress, Voice: *voice}, os.Stdout)
}

func shimCheckCmd(args []string) error {
	fs := flag.NewFlagSet("shimcheck", flag.ExitOnError)
	client := fs.Uint("client", 19906, "порт шима для клиентов")
	service := fs.Uint("service", 9906, "порт настоящего сервиса")
	src := fs.String("src", "127.0.0.2", "адрес, с которого шим ходит к сервису")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return usageError("shimcheck")
	}
	ip := net.ParseIP(*src)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("-src: не адрес IPv4: %q", *src)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	return devtools.ShimCheck{ClientPort: uint16(*client), ServicePort: uint16(*service), ShimAddr: ip}.Check(f, os.Stdout)
}

// pcmWatchCmd печатает каждую смену состояния PCM воспроизведения и trigger_time (момент запуска или остановки потока
// ядром, CLOCK_MONOTONIC) вместе с нашим монотонным временем. Вместе с `observe -mono` даёт одну шкалу времени.
func pcmWatchCmd(args []string) error {
	fs := flag.NewFlagSet("pcmwatch", flag.ExitOnError)
	dur := fs.Duration("for", 60*time.Second, "сколько наблюдать")
	fs.Parse(args)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	end := time.Now().Add(*dur)

	var buf [512]byte
	prev, prevTrig := "", int64(-1)
	for time.Now().Before(end) {
		select {
		case <-sig:
			return nil
		default:
		}
		state, trig, err := alsactl.Read(buf[:])
		if err != nil {
			return err
		}
		now := clock.Mono()
		if state != prev || trig != prevTrig {
			from := prev
			if from == "" {
				from = "?"
			}
			fmt.Printf("m%.4f PCM %s → %s, trigger_time m%.6f\n", float64(now)/1e9, from, state, float64(trig)/1e9)
			prev, prevTrig = state, trig
		}
		time.Sleep(500 * time.Microsecond)
	}
	return nil
}

func sanitizeCmd(args []string) error {
	fs := flag.NewFlagSet("sanitize", flag.ExitOnError)
	keepTitles := fs.Bool("keep-titles", false, "оставить названия, исполнителей и обложки треков")
	keepVoice := fs.Bool("keep-voice", false, "оставить звук ответов Алисы (по умолчанию — тишина той же длины)")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return usageError("sanitize")
	}
	out, err := os.Create(fs.Arg(1))
	if err != nil {
		return err
	}
	st, err := devtools.Sanitize(fs.Arg(0), out, devtools.SanitizeOpt{KeepTitles: *keepTitles, KeepVoice: *keepVoice})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(fs.Arg(1))
		return err
	}
	fmt.Printf("%s → %s: %s\nПеред публикацией просмотрите результат: openmediad-dev pcap -dump %s\n", fs.Arg(0), fs.Arg(1), st, fs.Arg(1))
	return nil
}
