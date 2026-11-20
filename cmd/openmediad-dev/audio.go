package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"openmediad/internal/components/soundcloud"
	"openmediad/internal/components/yandex-music"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/speaker"
)

// audioCommands — инструменты звука: проверка цепочек на колонке отдельно от шины и Алисы.
func audioCommands() []command {
	return []command{
		{"voice-play", "[-aplay путь] [-dev default] [-start-delay 1] [-prespawn] [-repeat N] [-chunk N] [-decode-only] файл.ogg",
			"на колонке: проиграть чанк голоса связкой «Go-декодер → pipe → aplay» и замерить задержку до запуска PCM", voicePlay},
		{"play-yandex", "-url URL [-key HEX] [-from мс] [-for 15s] [-gain дБ] [-silent]",
			"на колонке: один поток Яндекса через движок (HTTPS, AES, MP4, GStreamer, aplay) без шины и Алисы; без ключа и со ссылкой на .m3u8 — радио (HLS); -silent — без звука: PCM считается и выбрасывается", playYandex},
		{"play-soundcloud", "-link ССЫЛКА [-proxy http://хост:порт] [-for 15s] [-silent]",
			"на колонке: SoundCloud по ссылке через движок (client_id с сайта, HLS fMP4, GStreamer, aplay) без демона; -silent — без звука", playSoundCloud},
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
	silent := fs.Bool("silent", false, "не играть в динамики: считать PCM в темпе воспроизведения и выбрасывать (проверка без звука)")
	fs.Parse(args)
	k, err := hex.DecodeString(*key)
	radio := *key == "" && yandexmusic.IsRadio(yandexmusic.Track{URL: *url})
	if err != nil || len(k) != 16 && !radio || *url == "" {
		return usageError("play-yandex")
	}
	t0 := time.Now()
	logf := func(f string, a ...any) { fmt.Printf("+%6.3fs %s\n", time.Since(t0).Seconds(), fmt.Sprintf(f, a...)) }
	spk := &speaker.Sink{}
	null := &nullOutput{}
	if *silent {
		defer func() {
			logf("без звука: принято %d мс PCM, пик %d из 32767", null.ms(), null.peak.Load())
		}()
	}
	eng := &engine.Engine{Router: engine.RouterFunc(func(_ engine.Class, f audio.Format) (audio.Output, error) {
		if *silent {
			null.f = f
			return null, nil
		}
		return spk.Open(f)
	}), Logf: logf}
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

func playSoundCloud(args []string) error {
	fs := flag.NewFlagSet("play-soundcloud", flag.ExitOnError)
	link := fs.String("link", "", "ссылка на трек, плейлист, альбом или исполнителя SoundCloud")
	proxy := fs.String("proxy", "", "прокси для SoundCloud (http://хост:порт, socks5://хост:порт)")
	dur := fs.Duration("for", 15*time.Second, "сколько играть")
	silent := fs.Bool("silent", false, "не играть в динамики: считать PCM в темпе воспроизведения и выбрасывать (проверка без звука)")
	fs.Parse(args)
	if *link == "" {
		return usageError("play-soundcloud")
	}
	t0 := time.Now()
	logf := func(f string, a ...any) { fmt.Printf("+%6.3fs %s\n", time.Since(t0).Seconds(), fmt.Sprintf(f, a...)) }
	spk := &speaker.Sink{}
	null := &nullOutput{}
	if *silent {
		defer func() {
			logf("без звука: принято %d мс PCM, пик %d из 32767", null.ms(), null.peak.Load())
		}()
	}
	eng := &engine.Engine{Router: engine.RouterFunc(func(_ engine.Class, f audio.Format) (audio.Output, error) {
		if *silent {
			null.f = f
			return null, nil
		}
		return spk.Open(f)
	}), Logf: logf}
	c, err := soundcloud.NewStandalone(eng, *proxy, logf)
	if err != nil {
		return err
	}
	defer c.Shutdown()
	u, err := control.ParseLink(*link)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	title, err := c.PlayLink(ctx, u)
	cancel()
	if err != nil {
		return fmt.Errorf("ссылка не включилась: %w", err)
	}
	logf("включено: %s", title)
	end := time.After(*dur)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	first := false
	for {
		select {
		case <-tick.C:
			st := c.State()
			logf("%s; звучит %v, RSS %d кБ", c.StatusLine(), st.Sounding, rssKB())
			if !first && st.Sounding {
				first = true
				logf("звук пошёл через %s после ссылки", time.Since(t0).Round(time.Millisecond))
			}
			if st == (control.State{}) {
				logf("очередь доиграна или остановлена")
				return nil
			}
		case <-end:
			logf("время вышло, останавливаю")
			return nil
		}
	}
}

// nullOutput — вывод «в никуда» в темпе воспроизведения: считает принятый звук и его пик. Для проверок без звука.
type nullOutput struct {
	f     audio.Format
	bytes atomic.Uint64
	peak  atomic.Int32
}

func (n *nullOutput) Write(p []byte) (int, error) {
	var peak int32
	for i := 0; i+1 < len(p); i += 2 {
		v := int32(int16(uint16(p[i]) | uint16(p[i+1])<<8))
		if v < 0 {
			v = -v
		}
		peak = max(peak, v)
	}
	if peak > n.peak.Load() {
		n.peak.Store(peak)
	}
	n.bytes.Add(uint64(len(p)))
	time.Sleep(n.f.Duration(uint64(len(p))))
	return len(p), nil
}
func (n *nullOutput) Drain() {}
func (n *nullOutput) Abort() {}
func (n *nullOutput) ms() int64 {
	if n.f.Rate == 0 {
		return 0
	}
	return n.f.Duration(n.bytes.Load()).Milliseconds()
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
