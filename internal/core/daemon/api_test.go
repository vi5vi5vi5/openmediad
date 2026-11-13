package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/quasar/alicedclient"
)

// call выполняет запрос сокета прямо в демоне и возвращает первую строку ответа.
func call(t *testing.T, d *daemon, req ctlsock.Request, out any) error {
	t.Helper()
	return d.handle(context.Background(), req, func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
}

// apiDaemon — демон с плеером движка p и записывающим пультом платформы.
func apiDaemon(t *testing.T, p *controltest.Player) (*daemon, *remoteRig) {
	d := testDaemon(t)
	d.comps = []*component{} // «компоненты подключены»
	if p != nil {
		d.hub.Add("spotify", p)
	}
	g := newRemoteRig()
	d.hub.Remote = g.r
	d.api.ask = g.r.ask
	return d, g
}

// Команда извне идёт тем же путём, что кнопка наушников: играет плеер движка — выполняет он.
func TestAPIControlReachesEnginePlayer(t *testing.T) {
	p := controltest.NewPlayer(controltest.Playing())
	d, g := apiDaemon(t, p)
	var res ctlsock.ControlResult
	if err := call(t, d, ctlsock.Request{Cmd: "control", Action: "toggle"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.By != "spotify" || res.Action != "toggle" {
		t.Fatalf("ответ %+v", res)
	}
	if done := p.Done(); len(done) != 1 || done[0] != (controltest.Done{Cmd: control.Pause, From: control.API}) {
		t.Fatalf("плеер получил %v, ждали паузу извне", done)
	}
	if len(g.sent)+len(g.asked) != 0 {
		t.Fatal("платформе команда уходить не должна")
	}
}

// Плееры движка молчат: команда уходит музыке платформы, как нажатие кнопки на колонке.
func TestAPIControlFallsBackToPlatform(t *testing.T) {
	d, g := apiDaemon(t, controltest.NewPlayer(control.State{}))
	var res ctlsock.ControlResult
	if err := call(t, d, ctlsock.Request{Cmd: "control", Action: "next"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.By != control.OwnerPlatform || len(g.sent) != 1 || g.sent[0] != alicedclient.Next {
		t.Fatalf("ответ %+v, платформе ушло %v", res, g.sent)
	}
}

func TestAPIControlRejectsUnknownAction(t *testing.T) {
	d, _ := apiDaemon(t, nil)
	for _, a := range []string{"", "jump", "Пауза"} {
		if err := call(t, d, ctlsock.Request{Cmd: "control", Action: a}, nil); err == nil {
			t.Fatalf("действие %q должно быть отвергнуто", a)
		}
	}
	d.hub.Remote = nil
	if err := call(t, d, ctlsock.Request{Cmd: "control", Action: "pause"}, nil); err == nil || !strings.Contains(err.Error(), "некому") {
		t.Fatalf("без плееров и пульта команду выполнить некому: %v", err)
	}
}

// «Кто играет»: плееры движка с состоянием, музыка платформы, владелец звука, диалог.
func TestAPIPlayers(t *testing.T) {
	st := controltest.Playing()
	st.Track = "t1"
	d, _ := apiDaemon(t, controltest.NewPlayer(st))
	d.hub.SetOwner("spotify")
	d.focusChanged(true)
	var p ctlsock.Playing
	if err := call(t, d, ctlsock.Request{Cmd: "players"}, &p); err != nil {
		t.Fatal(err)
	}
	if p.Owner != "spotify" || !p.Dialog || p.PlatformPlaying {
		t.Fatalf("ответ %+v", p)
	}
	if len(p.Players) != 1 || p.Players[0].Name != "spotify" || !p.Players[0].Playing || !p.Players[0].Sounding || p.Players[0].Track != "t1" {
		t.Fatalf("плееры %+v", p.Players)
	}
}

// Подписчик получает события плееров (с отметкой, кто покажет анимацию) и диалога; после отключения рассылка его забывает.
func TestAPIEvents(t *testing.T) {
	d, _ := apiDaemon(t, controltest.NewPlayer(controltest.Playing()))
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan ctlsock.Event, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handle(ctx, ctlsock.Request{Cmd: "events"}, func(v any) error { got <- v.(ctlsock.Event); return nil })
	}()
	waitSubs := func(n int) {
		t.Helper()
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
			d.api.bus.mu.Lock()
			k := len(d.api.bus.subs)
			d.api.bus.mu.Unlock()
			if k == n {
				return
			}
		}
		t.Fatalf("подписчиков не %d", n)
	}
	waitSubs(1)
	d.hub.Command(control.Pause, control.Headphones)
	d.hub.Do(control.Next, control.FastVoice)
	d.focusChanged(true)
	d.focusChanged(false)
	want := []ctlsock.Event{
		{Type: ctlsock.EventPlayer, Action: "pause", From: "headphones", Player: "spotify"},
		{Type: ctlsock.EventPlayer, Action: "next", From: "fast", Player: "spotify", PlatformDisplay: true, PlatformRing: true},
		{Type: ctlsock.EventDialog, On: true},
		{Type: ctlsock.EventDialog},
	}
	for i, w := range want {
		select {
		case e := <-got:
			if e.AtMs == 0 {
				t.Fatalf("событие %d без времени", i)
			}
			e.AtMs = 0
			if e != w {
				t.Fatalf("событие %d: %+v, ждали %+v", i, e, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("не дождались события %d", i)
		}
	}
	cancel()
	<-done
	waitSubs(0)
}

// Фраза Алисе: непустая, не длиннее предела, уходит как есть.
func TestAPIAsk(t *testing.T) {
	d, g := apiDaemon(t, nil)
	if err := call(t, d, ctlsock.Request{Cmd: "ask", Text: "  включи свет "}, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.asked) != 1 || g.asked[0] != "включи свет" {
		t.Fatalf("Алисе ушло %q", g.asked)
	}
	if err := call(t, d, ctlsock.Request{Cmd: "ask", Text: "   "}, nil); err == nil {
		t.Fatal("пустую фразу надо отвергнуть")
	}
	if err := call(t, d, ctlsock.Request{Cmd: "ask", Text: strings.Repeat("я", 300)}, nil); err == nil {
		t.Fatal("слишком длинную фразу надо отвергнуть")
	}
}
