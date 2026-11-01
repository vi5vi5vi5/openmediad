package spotifysoloist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"openmediad/internal/libs/wsclient"
)

// WebSocket API Soloist (loopback, без авторизации): клиент шлёт {"type":"command","command":"имя", …поля}, сервер шлёт события.
// Ответа с номером запроса нет: command_result означает только «принято». Формат — docs/MUSIC-PLAN.md §10.2.

// event — объединение полей событий, которые нужны компоненту; остальное игнорируется.
type event struct {
	Type       string `json:"type"`
	LoggedIn   bool   `json:"logged_in"`
	IsActive   bool   `json:"is_active"`
	DeviceName string `json:"device_name"`
	Status     string `json:"status"`
	Volume     *int   `json:"volume"`
	Message    string `json:"message"`
	Item       *item  `json:"item"`
	Position   *struct {
		PositionMS int64 `json:"position_ms"`
	} `json:"position"`
	Options *struct {
		Shuffle bool `json:"shuffle"`
	} `json:"options"`
	Context *struct {
		Decorations decor `json:"decorations"`
	} `json:"context"`
}

type decor struct {
	Identity struct {
		Name string `json:"name"`
	} `json:"identity"`
}

type item struct {
	URI         string `json:"uri"`
	Decorations struct {
		Identity decor2 `json:"identity"`
		Creators []struct {
			Entity struct {
				Decorations decor `json:"decorations"`
			} `json:"entity"`
		} `json:"creators"`
		Parent struct {
			Entity struct {
				Decorations decor `json:"decorations"`
			} `json:"entity"`
		} `json:"parent"`
		Playback struct {
			DurationMS int64 `json:"duration_ms"`
		} `json:"playback"`
	} `json:"decorations"`
}

type decor2 = struct {
	Name string `json:"name"`
}

// playerState — что известно о воспроизведении (для статуса и арбитража).
type playerState struct {
	Connected  bool   `json:"connected"` // есть соединение с WebSocket Soloist
	LoggedIn   bool   `json:"logged_in"`
	Active     bool   `json:"active"` // это устройство выбрано в приложении Spotify
	DeviceName string `json:"device_name,omitempty"`
	Status     string `json:"status,omitempty"` // playing | paused | buffering | idle …
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	Context    string `json:"context,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	PositionMS int64  `json:"position_ms,omitempty"`
	// PositionAt — когда пришла позиция: Soloist присылает её только с событием состояния, между ними она устаревает.
	PositionAt time.Time `json:"-"`
	URI        string    `json:"uri,omitempty"` // spotify:track:… играющего трека
	Volume     int       `json:"volume"`        // 0–100, -1 — не сообщали
	Shuffle    bool      `json:"shuffle"`
	LastError  string    `json:"last_error,omitempty"`
}

// control — клиент WebSocket API Soloist: держит соединение, разбирает события, шлёт команды.
type control struct {
	addr string
	logf func(string, ...any)
	// dial подменяется в тестах.
	dial func(ctx context.Context, addr string) (*wsclient.Conn, error)

	mu      sync.Mutex
	conn    *wsclient.Conn
	st      playerState
	changed func() // состояние изменилось
}

func newControl(addr string, logf func(string, ...any)) *control {
	c := &control{addr: addr, logf: logf, st: playerState{Volume: -1}}
	c.dial = func(ctx context.Context, addr string) (*wsclient.Conn, error) { return wsclient.Dial(ctx, addr, "/") }
	return c
}

func (c *control) state() playerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// run держит соединение до отмены ctx: Soloist открывает порт через секунду после старта и закрывает его при перезапуске.
func (c *control) run(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		conn, err := c.dial(dctx, c.addr)
		cancel()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 500 * time.Millisecond
		c.session(ctx, conn)
	}
}

func (c *control) session(ctx context.Context, conn *wsclient.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.st = playerState{Connected: true, Volume: -1}
	c.mu.Unlock()
	c.notify()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.WriteText(`{"type":"command","command":"get_auth_state"}`)
	conn.WriteText(`{"type":"command","command":"get_state"}`)
	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				c.logf("WebSocket Soloist: %v", err)
			}
			break
		}
		c.handle(msg)
	}
	conn.Close()
	c.mu.Lock()
	c.conn = nil
	c.st = playerState{Volume: -1}
	c.mu.Unlock()
	c.notify()
}

func (c *control) notify() {
	if c.changed != nil {
		c.changed()
	}
}

// handle разбирает одно событие.
func (c *control) handle(raw []byte) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return
	}
	c.mu.Lock()
	switch e.Type {
	case "auth_state":
		c.st.LoggedIn, c.st.Active, c.st.DeviceName = e.LoggedIn, e.IsActive, e.DeviceName
	case "playback_state":
		c.st.Status, c.st.Active = e.Status, e.IsActive
		if e.Volume != nil {
			c.st.Volume = *e.Volume
		}
		if e.Position != nil {
			c.st.PositionMS = e.Position.PositionMS
			c.st.PositionAt = time.Now()
		}
		if e.Options != nil {
			c.st.Shuffle = e.Options.Shuffle
		}
		if e.Context != nil {
			c.st.Context = e.Context.Decorations.Identity.Name
		}
		if it := e.Item; it != nil {
			c.st.URI = it.URI
			c.st.Title = it.Decorations.Identity.Name
			c.st.Artist = ""
			if len(it.Decorations.Creators) > 0 {
				names := make([]string, 0, len(it.Decorations.Creators))
				for _, cr := range it.Decorations.Creators {
					names = append(names, cr.Entity.Decorations.Identity.Name)
				}
				c.st.Artist = strings.Join(names, ", ")
			}
			c.st.Album = it.Decorations.Parent.Entity.Decorations.Identity.Name
			c.st.DurationMS = it.Decorations.Playback.DurationMS
		}
	case "volume_changed":
		if e.Volume != nil {
			c.st.Volume = *e.Volume
		}
	case "error":
		c.st.LastError = e.Message
	}
	c.mu.Unlock()
	if e.Type == "error" && e.Message != "" {
		c.logf("Soloist: %s", e.Message)
	}
	c.notify()
}

// send шлёт команду Soloist. Ответа о выполнении нет: смотреть результат нужно по событиям.
func (c *control) send(command string, fields map[string]any) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("нет соединения с Soloist")
	}
	m := map[string]any{"type": "command", "command": command}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return conn.WriteText(string(b))
}

// Pause ставит воспроизведение на паузу (так же, как кнопка в приложении Spotify).
func (c *control) Pause() error { return c.send("pause", nil) }

// Resume продолжает воспроизведение. У Soloist нет отдельной команды: play без uri.
func (c *control) Resume() error { return c.send("play", nil) }

// SkipNext, SkipPrev — следующий и предыдущий трек.
func (c *control) SkipNext() error { return c.send("skip_next", nil) }
func (c *control) SkipPrev() error { return c.send("skip_prev", nil) }

// PlayURI запускает контекст (плейлист, альбом, исполнитель) или трек.
func (c *control) PlayURI(uri string) error { return c.send("play", map[string]any{"uri": uri}) }

// SetVolume задаёт громкость Soloist, 0–100.
func (c *control) SetVolume(v int) error {
	if v < 0 || v > 100 {
		return fmt.Errorf("громкость %d вне 0–100", v)
	}
	return c.send("set_volume", map[string]any{"volume": v})
}
