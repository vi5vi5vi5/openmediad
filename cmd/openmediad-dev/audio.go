package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"openmediad/internal/components/yandex-music"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/speaker"
)

// audioCommands — инструменты звука: проверка цепочек на колонке отдельно от шины и Алисы.
func audioCommands() []command {
	return []command{
		{"voice-play", "[-aplay путь] [-dev default] [-start-delay 1] [-prespawn] [-repeat N] [-chunk N] [-decode-only] файл.ogg",
			"на колонке: проиграть чанк голоса связкой «Go-декодер → pipe → aplay» и замерить задержку до запуска PCM", voicePlay},
		{"play-yandex", "-url URL -key HEX [-from мс] [-for 15s] [-gain дБ]",
			"на колонке: один поток Яндекса через движок (HTTPS, AES, MP4, GStreamer, aplay) без шины и Алисы", playYandex},
	}
}

// musicEvents печатает события привязки музыки.
type musicEvents struct {
	logf     func(string, ...any)
	opened   chan uint64
	failed   chan error
	finished chan struct{}
}

func (m *musicEvents) Opened(_ string, d uint64)  { m.opened <- d }
func (m *musicEvents) Failed(_ string, err error) { m.failed <- err }
func (m *musicEvents) Finished(string)            { m.finished <- struct{}{} }

func playYandex(args []string) error {
	fs := flag.NewFlagSet("play-yandex", flag.ExitOnError)
	url := fs.String("url", "", "URL потока (поле 1.2 команды play)")
	key := fs.String("key", "", "ключ AES-128, 32 hex-символа (поле 1.39 команды)")
	from := fs.Uint64("from", 0, "начать с позиции, мс")
	dur := fs.Duration("for", 15*time.Second, "сколько играть")
	gain := fs.Float64("gain", 0, "поправка громкости трека, дБ (нормализация: −14 − громкость трека)")
	fs.Parse(args)
	k, err := hex.DecodeString(*key)
	if err != nil || len(k) != 16 || *url == "" {
		return usageError("play-yandex")
	}
	t0 := time.Now()
	logf := func(f string, a ...any) { fmt.Printf("+%6.3fs %s\n", time.Since(t0).Seconds(), fmt.Sprintf(f, a...)) }
	spk := &speaker.Sink{}
	eng := &engine.Engine{Router: engine.RouterFunc(func(_ engine.Class, f audio.Format) (audio.Output, error) { return spk.Open(f) }), Logf: logf}
	ev := &musicEvents{logf: logf, opened: make(chan uint64, 1), failed: make(chan error, 1), finished: make(chan struct{}, 1)}
	b := &yandexmusic.Backend{Engine: eng, Events: ev, Logf: logf}
	defer b.Shutdown()

	b.Open("test", mediad.Track{ID: "test", URL: *url, Key: k, Title: "play-yandex", GainDB: *gain}, *from)
	select {
	case d := <-ev.opened:
		logf("поток открыт: длительность %d мс, первые 300 мс звука готовы", d)
	case err := <-ev.failed:
		return fmt.Errorf("поток не открылся: %w", err)
	case <-time.After(30 * time.Second):
		return fmt.Errorf("за 30 с поток не открылся")
	}
	start := time.Now()
	b.Start("test")
	end := time.After(*dur)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	first := false
	for {
		select {
		case <-tick.C:
			got, tot := b.Net("test")
			logf("позиция %d мс, скачано %d из %d Б, RSS %d кБ", b.Pos("test"), got, tot, rssKB())
		case <-ev.finished:
			logf("трек доигран до конца")
			return nil
		case <-end:
			logf("время вышло (%s), останавливаю", time.Since(start).Round(time.Second))
			return nil
		}
		if !first && b.Pos("test") > *from {
			first = true
			logf("звук пошёл через %s после Start", time.Since(start).Round(time.Millisecond))
		}
	}
}

func rssKB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	var kb int
	for _, l := range splitLines(string(b)) {
		if _, err := fmt.Sscanf(l, "VmRSS: %d kB", &kb); err == nil {
			return kb
		}
	}
	return 0
}

func splitLines(s string) (out []string) {
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
