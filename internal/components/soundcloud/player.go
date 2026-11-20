package soundcloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/led"
	"openmediad/internal/core/spectrum"
	"openmediad/internal/libs/aac"
)

// Плеер SoundCloud: очередь треков (один трек, плейлист или треки исполнителя по ссылке) и по потоку движка на трек. Команды «пауза»,
// «дальше», «назад» приносит ядро (control.Player), ссылку — тоже ядро (control.LinkPlayer). Платформа про этот плеер не знает: Яндекс
// ставится на паузу уступкой звука ядра, когда SoundCloud зазвучал.

const (
	Name = "soundcloud"

	maxFails    = 3                // столько треков подряд не открылось — очередь останавливается
	prevRestart = 3 * time.Second  // «назад» позже этого от начала трека — тот же трек сначала
	nearEnd     = 4 * time.Second  // трек доигрывается
	openTimeout = 20 * time.Second // сколько ждать адрес потока трека
)

// ledColor — цвет SoundCloud на кольце подсветки (меняется в настройках led-ring).
var ledColor = led.Color{R: 0xFF, G: 0x55, B: 0x00}

type status int

const (
	idle    status = iota // очереди нет или она кончилась
	loading               // трек открывается
	playing
	paused
)

func (s status) String() string {
	return [...]string{"ничего не играет", "открывает трек", "играет", "на паузе"}[s]
}

// Component — компонент soundcloud.
type Component struct {
	cfg    Config
	eng    *engine.Engine
	api    *api
	led    led.Indicator
	logf   func(string, ...any)
	client *http.Client
	newDec aac.Factory
	// newSource — источник трека (тесты подменяют: без HLS и GStreamer).
	newSource func(t track, streamURL string) engine.Source

	mu       sync.Mutex
	queue    []track
	pos      int
	what     string // что включено, для человека
	st       *engine.Stream
	gen      int // номер запуска трека: события старых потоков отбрасываются
	state    status
	sounding bool
	heard    bool // у текущего потока уже был первый звук (Events.Playing приходит один раз за поток, а после паузы звук идёт сразу)
	fails    int
	played   int
	lastErr  string
}

func (c *Component) source(t track, streamURL string) engine.Source {
	if c.newSource != nil {
		return c.newSource(t, streamURL)
	}
	return newTrackSource(streamURL, time.Duration(t.DurationMs)*time.Millisecond, c.client, c.newDec, func(f string, a ...any) {
		c.logf("трек %d: %s", t.ID, fmt.Sprintf(f, a...))
	})
}

// ---- музыка по ссылке (control.LinkPlayer) ----

// LinkHosts — домены SoundCloud.
func (c *Component) LinkHosts() []string {
	return []string{"soundcloud.com", "on.soundcloud.com", "soundcloud.app.goo.gl"}
}

// canonical приводит ссылку к виду, который понимает resolve: https://soundcloud.com/путь, без меток (?si=…, utm).
func canonical(u *url.URL) string {
	return "https://soundcloud.com" + strings.TrimSuffix(u.EscapedPath(), "/")
}

// PlayLink включает трек, плейлист (альбом) или треки исполнителя.
func (c *Component) PlayLink(ctx context.Context, u *url.URL) (string, error) {
	if h := strings.ToLower(u.Hostname()); h == "on.soundcloud.com" || h == "soundcloud.app.goo.gl" {
		var err error
		if u, err = c.api.expand(ctx, u); err != nil {
			return "", err
		}
	}
	if strings.Trim(u.Path, "/") == "" {
		return "", errors.New("в ссылке SoundCloud нет трека, плейлиста или исполнителя")
	}
	it, err := c.api.resolve(ctx, canonical(u))
	if err != nil {
		return "", err
	}
	var list []track
	var what string
	switch it.Kind {
	case "track":
		list, what = []track{it.Track}, it.Track.Name()
	case "playlist", "system-playlist":
		if list, err = c.api.fillTracks(ctx, it.Tracks); err != nil {
			return "", err
		}
		kind := "плейлист"
		if it.IsAlbum {
			kind = "альбом"
		}
		what = kind + " «" + it.Title + "»"
	case "user":
		if list, err = c.api.userTracks(ctx, it.User.ID, c.cfg.MaxTracks); err != nil {
			return "", err
		}
		what = "треки " + it.User.Username
	default:
		return "", fmt.Errorf("ссылка SoundCloud ведёт на «%s»: включить можно трек, плейлист, альбом или исполнителя", it.Kind)
	}
	if len(list) > c.cfg.MaxTracks {
		list = list[:c.cfg.MaxTracks]
	}
	ok := list[:0:0]
	var why error
	for _, t := range list {
		if err := t.playable(); err != nil {
			if why == nil {
				why = fmt.Errorf("«%s»: %w", t.Name(), err)
			}
			continue
		}
		ok = append(ok, t)
	}
	if len(ok) == 0 {
		if why == nil {
			why = errors.New("в нём нет треков")
		}
		return "", fmt.Errorf("SoundCloud: нечего играть — %w", why)
	}
	c.mu.Lock()
	c.queue, c.what, c.fails, c.lastErr = ok, what, 0, ""
	c.mu.Unlock()
	c.play(0)
	c.logf("включено по ссылке: %s, треков %d", it.Kind, len(ok)) // названия в журнал не пишем: что слушают — личное
	if it.Kind == "track" {
		return what, nil
	}
	return fmt.Sprintf("%s, %s, первым — %s", what, tracksWord(len(ok)), ok[0].Name()), nil
}

// tracksWord — «1 трек», «3 трека», «12 треков».
func tracksWord(n int) string {
	w := "треков"
	switch {
	case n%100 >= 11 && n%100 <= 14:
	case n%10 == 1:
		w = "трек"
	case n%10 >= 2 && n%10 <= 4:
		w = "трека"
	}
	return strconv.Itoa(n) + " " + w
}

// ---- очередь и потоки ----

// play запускает трек очереди с номером i (старый поток закрывается).
func (c *Component) play(i int) {
	c.mu.Lock()
	if i < 0 || i >= len(c.queue) {
		c.mu.Unlock()
		return
	}
	c.gen++
	gen, old, t := c.gen, c.st, c.queue[i]
	c.st, c.pos, c.state, c.sounding, c.heard = nil, i, loading, false, false
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	go c.open(gen, t)
}

// open получает адрес потока и открывает поток движка; играть он начнёт, когда источник будет готов (ready).
func (c *Component) open(gen int, t track) {
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	u, err := c.api.streamURL(ctx, t)
	if err != nil {
		c.failed(gen, err)
		return
	}
	st := c.eng.NewStream(fmt.Sprintf("%s:%d", Name, t.ID), engine.Music, c.source(t, u),
		engine.Options{Spectrum: spectrum.SourceMusic, RewindOnPause: true, FadeIn: 25 * time.Millisecond},
		engine.Events{
			Ready:   func(audio.Info) { c.ready(gen) },
			Playing: func() { c.sound(gen) },
			Ended:   func() { go c.ended(gen) },
			Failed:  func(err error) { go c.failed(gen, err) },
		})
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		st.Close()
		return
	}
	c.st = st
	c.mu.Unlock()
	st.Open()
}

// ready — источник открыт: играть, если за это время не поставили на паузу.
func (c *Component) ready(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen && c.st != nil && c.state == loading {
		c.state = playing
		c.st.Play()
	}
}

// sound — первый звук трека.
func (c *Component) sound(gen int) {
	c.mu.Lock()
	if c.gen != gen || c.state != playing {
		c.mu.Unlock()
		return
	}
	c.sounding, c.heard, c.fails = true, true, 0
	c.played++
	c.mu.Unlock()
	if c.led != nil {
		c.led.On(led.Source, Name, ledColor)
	}
}

// ended — трек доигран: следующий или конец очереди.
func (c *Component) ended(gen int) {
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		return
	}
	next := c.pos + 1
	if next < len(c.queue) {
		c.mu.Unlock()
		c.play(next)
		return
	}
	c.stopLocked()
	c.mu.Unlock()
	c.ledOff()
	c.logf("очередь доиграна")
}

// failed — трек не открылся: следующий (не больше maxFails подряд).
func (c *Component) failed(gen int, err error) {
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		return
	}
	c.fails++
	c.lastErr = err.Error()
	next := c.pos + 1
	if c.fails < maxFails && next < len(c.queue) {
		c.mu.Unlock()
		c.logf("трек не открылся (%v): следующий", err)
		c.play(next)
		return
	}
	c.stopLocked()
	c.mu.Unlock()
	c.ledOff()
	c.logf("трек не открылся (%v): очередь остановлена", err)
}

// stopLocked закрывает поток и забывает очередь (c.mu взят).
func (c *Component) stopLocked() {
	c.gen++
	if c.st != nil {
		go c.st.Close()
	}
	c.st, c.state, c.sounding = nil, idle, false
}

func (c *Component) ledOff() {
	if c.led != nil {
		c.led.Off(led.Source, Name)
	}
}

// ---- плеер ядра (control.Player) ----

// Accepts — плеер слушается всех источников: включён он только ссылкой, и пауза или «дальше» откуда угодно — ожидаемо.
func (c *Component) Accepts(control.Origin) bool { return true }

// State — что с плеером сейчас (без сети и ожидания).
func (c *Component) State() control.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == idle || len(c.queue) == 0 {
		return control.State{}
	}
	t := c.queue[c.pos]
	s := control.State{
		Ready:     true,
		Playing:   c.state == loading || c.state == playing,
		Sounding:  c.sounding && c.state == playing,
		Paused:    c.state == paused,
		Resumable: c.state == paused,
		Track:     fmt.Sprintf("%d#%d", t.ID, c.pos),
		Title:     t.Name(),
	}
	if d := time.Duration(t.DurationMs) * time.Millisecond; d > 0 && c.st != nil && c.state == playing {
		s.NearEnd = c.st.Pos() > d-nearEnd
	}
	return s
}

// Do выполняет команду ядра.
func (c *Component) Do(cmd control.Cmd, _ control.Origin) error {
	c.mu.Lock()
	if c.state == idle || len(c.queue) == 0 {
		c.mu.Unlock()
		return errors.New("SoundCloud ничего не играет")
	}
	switch cmd {
	case control.Pause:
		if c.state == playing && c.st != nil {
			c.st.Pause()
		}
		c.state, c.sounding = paused, false
		c.mu.Unlock()
		c.ledOff()
		return nil
	case control.Resume:
		on := false
		if c.state == paused {
			c.state = loading
			if c.st != nil {
				if _, ok := c.st.Ready(); ok {
					c.state, c.sounding, on = playing, c.heard, c.heard
					c.st.Play()
				}
			}
		}
		c.mu.Unlock()
		if on && c.led != nil {
			c.led.On(led.Source, Name, ledColor)
		}
		return nil
	case control.Next:
		next := c.pos + 1
		c.mu.Unlock()
		if next >= c.queueLen() {
			return errors.New("дальше в очереди SoundCloud ничего нет")
		}
		c.play(next)
		return nil
	case control.Prev:
		prev := c.pos - 1
		if c.st != nil && c.st.Pos() > prevRestart || prev < 0 {
			prev = c.pos
		}
		c.mu.Unlock()
		c.play(prev)
		return nil
	}
	c.mu.Unlock()
	return fmt.Errorf("команда %q не передаётся", cmd)
}

func (c *Component) queueLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// Shutdown закрывает поток при выходе демона.
func (c *Component) Shutdown() {
	c.mu.Lock()
	c.stopLocked()
	c.mu.Unlock()
	c.ledOff()
}

// StatusLine — строка для `openmediad status`.
func (c *Component) StatusLine() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == idle || len(c.queue) == 0 {
		if c.lastErr != "" {
			return "ничего не играет; последняя ошибка: " + c.lastErr
		}
		return "ничего не играет (включается ссылкой: openmediad player link …, !play в MeetUp)"
	}
	line := fmt.Sprintf("%s «%s»", c.state, c.queue[c.pos].Name())
	if len(c.queue) > 1 {
		line += fmt.Sprintf(" (%d из %d, %s)", c.pos+1, len(c.queue), c.what)
	}
	return line
}

// DebugState — для status -v и отладочной страницы.
func (c *Component) DebugState() any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"state": c.state.String(), "queue": len(c.queue), "pos": c.pos, "played": c.played, "last_error": c.lastErr,
		"client_id": map[bool]string{true: "из настроек", false: "находится на сайте"}[c.cfg.ClientID != ""],
	}
}
