package btout

import (
	"errors"
	"strings"
	"testing"

	"openmediad/internal/core/control"
	"openmediad/internal/libs/aliced-client"
)

type routePlayer struct {
	take bool
	got  []control.Cmd
	from []control.Origin
}

func (p *routePlayer) Handle(c control.Cmd, from control.Origin) bool {
	p.got = append(p.got, c)
	p.from = append(p.from, from)
	return p.take
}

// routeRig — маршрут нажатий с записью того, что ушло платформе.
type routeRig struct {
	r      *buttonRoute
	asked  []string
	sent   []alicedclient.Command
	logged []string
	yandex bool
	fail   error
}

func newRouteRig(players ...*routePlayer) *routeRig {
	h := &control.Hub{}
	for _, p := range players {
		h.Add("плеер", p)
	}
	g := &routeRig{}
	g.r = &buttonRoute{
		Hub:     h,
		Playing: func() bool { return g.yandex },
		Ask:     func(s string) error { g.asked = append(g.asked, s); return g.fail },
		Send:    func(c alicedclient.Command) error { g.sent = append(g.sent, c); return g.fail },
		Logf:    func(f string, a ...any) { g.logged = append(g.logged, f) },
	}
	return g
}

// Плеер движка (Spotify) взял команду — платформа о нажатии не узнаёт.
func TestPlayerGetsTheButtonFirstAndPlatformStaysOut(t *testing.T) {
	p := &routePlayer{take: true}
	g := newRouteRig(p)
	for b, want := range map[Button]control.Cmd{ButtonPlayPause: control.Toggle, ButtonStop: control.Pause, ButtonNext: control.Next, ButtonPrev: control.Prev} {
		p.got, p.from = nil, nil
		g.r.press(b)
		if len(p.got) != 1 || p.got[0] != want || p.from[0] != control.Headphones {
			t.Fatalf("%s: плеер получил %v от %v, ждали %q из наушников", b, p.got, p.from, want)
		}
	}
	if len(g.asked) != 0 || len(g.sent) != 0 {
		t.Fatalf("платформе ушло %v %v, хотя команду выполнил плеер", g.asked, g.sent)
	}
}

// Плеер движка отказался (играет Яндекс или ничего): всё как раньше. Пауза при музыке Яндекса — фразой, остальное — командой плееру платформы.
func TestPlatformGetsTheButtonWhenNoPlayerTakesIt(t *testing.T) {
	g := newRouteRig(&routePlayer{})
	g.yandex = true
	g.r.press(ButtonPlayPause)
	g.r.press(ButtonStop)
	if len(g.asked) != 2 || g.asked[0] != "поставь на паузу" || len(g.sent) != 0 {
		t.Fatalf("пауза при музыке Яндекса: фразы %v, команды %v", g.asked, g.sent)
	}
	g.asked = nil
	g.yandex = false
	g.r.press(ButtonPlayPause)
	g.r.press(ButtonNext)
	g.r.press(ButtonPrev)
	g.r.press(ButtonStop)
	want := []alicedclient.Command{alicedclient.Toggle, alicedclient.Next, alicedclient.Prev, alicedclient.Pause}
	if len(g.asked) != 0 || len(g.sent) != len(want) {
		t.Fatalf("фразы %v, команды %v", g.asked, g.sent)
	}
	for i, c := range want {
		if g.sent[i] != c {
			t.Fatalf("команда %d: %v, ждали %v", i, g.sent[i], c)
		}
	}
}

// Без ядра (Hub'а нет) наушники работают как до появления плееров движка.
func TestNoHubMeansPlatformOnly(t *testing.T) {
	g := newRouteRig()
	g.r.Hub = nil
	g.r.press(ButtonNext)
	if len(g.sent) != 1 || g.sent[0] != alicedclient.Next {
		t.Fatalf("команды %v", g.sent)
	}
}

func TestPlatformErrorIsLoggedNotFatal(t *testing.T) {
	g := newRouteRig()
	g.fail = errors.New("нет связи")
	g.r.press(ButtonNext)
	if len(g.logged) != 1 || !strings.Contains(g.logged[0], "не передана") {
		t.Fatalf("журнал: %v", g.logged)
	}
}
