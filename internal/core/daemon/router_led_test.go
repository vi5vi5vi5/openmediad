package daemon

import (
	"testing"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/led"
)

// ledSink — выход, который сам предлагает цвет подсветки (как наушники).
type ledSink struct {
	fakeSink
	color led.Color
}

func (s *ledSink) LEDColor() led.Color { return s.color }

func ledRouter(t *testing.T) (*router, *led.Panel, *ledSink, *fakeSink) {
	t.Helper()
	sp := &fakeSink{name: "speaker"}
	sp.avail.Store(true)
	bt := &ledSink{fakeSink: fakeSink{name: "bt-out"}, color: led.Color{B: 255}}
	bt.avail.Store(true)
	rt := newRouter(config.Route{Music: []string{"bt-out", "speaker"}, Voice: []string{"bt-out", "speaker"}, Alert: []string{"speaker"}}, sp)
	rt.add(bt)
	p := led.NewPanel()
	rt.ind = p
	return rt, p, bt, sp
}

func TestRouterReportsMusicOutputToTheLEDPanel(t *testing.T) {
	rt, p, _, _ := ledRouter(t)
	out, err := rt.Output(engine.Music, audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.State(); s.Output == nil || s.Output.Name != "bt-out" || s.Output.Default != (led.Color{B: 255}) {
		t.Fatalf("панель после открытия: %+v", s.Output)
	}
	out.Abort()
	if s := p.State(); s.Output != nil {
		t.Fatalf("после обрыва вывода отметка осталась: %+v", s.Output)
	}
	out.Abort() // повторный вызов безвреден
	out.Drain()
}

func TestRouterCountsStreamsPerOutput(t *testing.T) {
	rt, p, _, _ := ledRouter(t)
	a, _ := rt.Output(engine.Music, audio.Stereo48)
	b, _ := rt.Output(engine.Music, audio.Stereo48)
	a.Drain()
	if p.State().Output == nil {
		t.Fatal("один поток ещё играет, а отметка снята")
	}
	b.Abort()
	if p.State().Output != nil {
		t.Fatal("потоков нет, а отметка осталась")
	}
}

// Голос Алисы и звонки идут по своим маршрутам, но подсветка показывает, куда идёт музыка.
func TestRouterIgnoresVoiceAndAlertForTheLEDPanel(t *testing.T) {
	rt, p, _, _ := ledRouter(t)
	v, _ := rt.Output(engine.Voice, audio.Stereo48)
	al, _ := rt.Output(engine.Alert, audio.Stereo48)
	if s := p.State(); s.Output != nil {
		t.Fatalf("голос или звонок попали на панель: %+v", s.Output)
	}
	v.Abort()
	al.Abort()
}

func TestRouterSpeakerHasNoSuggestedColorButIsReported(t *testing.T) {
	rt, p, bt, _ := ledRouter(t)
	bt.avail.Store(false) // наушников нет — музыка на динамиках
	out, _ := rt.Output(engine.Music, audio.Stereo48)
	if s := p.State(); s.Output == nil || s.Output.Name != "speaker" || !s.Output.Default.IsZero() {
		t.Fatalf("динамики на панели: %+v", s.Output)
	}
	out.Abort()
}

func TestRouterWorksWithoutPanel(t *testing.T) {
	sp := &fakeSink{name: "speaker"}
	sp.avail.Store(true)
	rt := newRouter(config.Route{Music: []string{"speaker"}, Voice: []string{"speaker"}, Alert: []string{"speaker"}}, sp)
	out, err := rt.Output(engine.Music, audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := out.(*indicatedOutput); wrapped {
		t.Fatal("без панели вывод не должен оборачиваться")
	}
}
