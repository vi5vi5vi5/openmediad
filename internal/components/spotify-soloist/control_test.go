package spotifysoloist

import (
	"context"
	"strings"
	"testing"
	"time"
)

// События — в том виде, как их шлёт Soloist (docs/MUSIC-PLAN.md §10.2; снято с колонки 31.10.2026).
const (
	evAuth    = `{"type":"auth_state","logged_in":true,"is_active":true,"device_name":"omd-o3"}`
	evPlaying = `{"type":"playback_state","status":"playing","item":{"uri":"spotify:track:1","entity_type":"track","decorations":{` +
		`"identity":{"name":"Песня"},"parent":{"entity":{"decorations":{"identity":{"name":"Альбом"}}}},` +
		`"creators":[{"entity":{"decorations":{"identity":{"name":"Группа"}}}},{"entity":{"decorations":{"identity":{"name":"Гость"}}}}],` +
		`"playback":{"duration_ms":161123}}},"context":{"uri":"spotify:playlist:1","decorations":{"identity":{"name":"505: радио"}}},` +
		`"position":{"position_ms":82273,"speed":1},"volume":62,"is_active":true,"options":{"shuffle":true}}`
)

func TestHandleEvents(t *testing.T) {
	c := newControl("", func(string, ...any) {})
	changes := 0
	c.changed = func() { changes++ }
	c.handle([]byte(evAuth))
	c.handle([]byte(evPlaying))
	c.handle([]byte(`{"type":"volume_changed","volume":70}`))
	c.handle([]byte(`{"type":"position_sync","position":{"position_ms":1}}`)) // этот тип не разбирается
	c.handle([]byte(`не json`))
	st := c.state()
	if st.PositionAt.IsZero() {
		t.Fatal("время получения позиции не записано")
	}
	st.PositionAt = time.Time{} // время сравнивать нечем
	want := playerState{LoggedIn: true, Active: true, DeviceName: "omd-o3", Status: "playing", Title: "Песня", Artist: "Группа, Гость",
		Album: "Альбом", Context: "505: радио", DurationMS: 161123, PositionMS: 82273, URI: "spotify:track:1", Volume: 70, Shuffle: true}
	if st != want {
		t.Fatalf("состояние\n got %+v\nwant %+v", st, want)
	}
	if changes != 4 {
		t.Fatalf("уведомлений об изменении %d, ждали 4", changes)
	}
	c.handle([]byte(`{"type":"error","message":"нет ключа"}`))
	if got := c.state().LastError; got != "нет ключа" {
		t.Fatalf("ошибка Soloist не сохранена: %q", got)
	}
}

func TestControlSessionAndCommands(t *testing.T) {
	srv := startWS(t)
	c := newControl(srv.addr(), func(string, ...any) {})
	connected := make(chan struct{}, 4)
	c.changed = func() { connected <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// при подключении клиент сам спрашивает состояние
	for _, want := range []string{`"get_auth_state"`, `"get_state"`} {
		select {
		case m := <-srv.recv:
			if !strings.Contains(m, want) {
				t.Fatalf("первая команда %s, ждали %s", m, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("клиент не прислал запросов состояния")
		}
	}
	srv.push(t, evAuth)
	waitFor(t, "auth_state разобран", func() bool { return c.state().LoggedIn && c.state().Connected })

	if err := c.Pause(); err != nil {
		t.Fatal(err)
	}
	if m := <-srv.recv; m != `{"command":"pause","type":"command"}` {
		t.Fatalf("pause ушёл как %s", m)
	}
	c.Resume()
	if m := <-srv.recv; !strings.Contains(m, `"command":"play"`) || strings.Contains(m, "uri") {
		t.Fatalf("Resume должен быть play без uri: %s", m)
	}
	c.SetVolume(40)
	if m := <-srv.recv; !strings.Contains(m, `"command":"set_volume"`) || !strings.Contains(m, `"volume":40`) {
		t.Fatalf("set_volume: %s", m)
	}
	if err := c.SetVolume(101); err == nil {
		t.Fatal("громкость 101 принята")
	}
	c.SkipNext()
	if m := <-srv.recv; !strings.Contains(m, `"skip_next"`) {
		t.Fatalf("skip_next: %s", m)
	}
}

func TestSendWithoutConnection(t *testing.T) {
	c := newControl("127.0.0.1:1", func(string, ...any) {})
	if err := c.Pause(); err == nil {
		t.Fatal("команда без соединения не вернула ошибку")
	}
}

func TestControlReconnects(t *testing.T) {
	srv := startWS(t)
	c := newControl(srv.addr(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "соединение", func() bool { return c.state().Connected })
	<-srv.recv
	<-srv.recv
	srv.mu.Lock()
	srv.c.Close() // Soloist перезапустился
	srv.c = nil
	srv.mu.Unlock()
	waitFor(t, "переподключение", func() bool { return srv.conns.Load() == 2 && c.state().Connected })
	<-srv.recv // после переподключения клиент снова спрашивает состояние
	<-srv.recv
}
