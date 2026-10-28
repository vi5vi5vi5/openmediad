package app

import (
	"errors"
	"sync/atomic"
	"testing"

	"openmediad/internal/audio"
	"openmediad/internal/config"
	"openmediad/internal/engine"
)

type nopOut struct{ sink string }

func (nopOut) Write(p []byte) (int, error) { return len(p), nil }
func (nopOut) Drain()                      {}
func (nopOut) Abort()                      {}

type fakeSink struct {
	name  string
	avail atomic.Bool
	fail  bool
}

func (s *fakeSink) Name() string    { return s.name }
func (s *fakeSink) Available() bool { return s.avail.Load() }
func (s *fakeSink) Open(audio.Format) (audio.Output, error) {
	if s.fail {
		return nil, errors.New("не открылся")
	}
	return nopOut{s.name}, nil
}

// Выход компонента (наушники) подключается при запуске компонентов: музыка идёт в него, пока он доступен, иначе — на
// динамики; голос по своему маршруту остаётся на динамиках.
func TestRouterUsesComponentSinkWhileAvailable(t *testing.T) {
	sp := &fakeSink{name: "speaker"}
	sp.avail.Store(true)
	rt := newRouter(config.Route{Music: []string{"bluetooth", "speaker"}, Voice: []string{"speaker"}, Alert: []string{"speaker"}}, sp)
	out := func(c engine.Class) string {
		o, err := rt.Output(c, audio.Stereo48)
		if err != nil {
			t.Fatal(err)
		}
		return o.(nopOut).sink
	}
	if out(engine.Music) != "speaker" {
		t.Fatal("выхода bluetooth ещё нет — музыка на динамиках")
	}
	bt := &fakeSink{name: "bluetooth"}
	rt.add(bt)
	if out(engine.Music) != "speaker" {
		t.Fatal("наушники не подключены — музыка на динамиках")
	}
	bt.avail.Store(true)
	if out(engine.Music) != "bluetooth" || out(engine.Voice) != "speaker" {
		t.Fatal("музыка — в наушники, голос — на динамики")
	}
	bt.fail = true
	if out(engine.Music) != "speaker" {
		t.Fatal("наушники не открылись — следующий выход маршрута")
	}
}

// Подключились наушники: переводить в них стоит только классы, у которых они в маршруте раньше доступного выхода.
func TestRouterBetter(t *testing.T) {
	sp, bt := &fakeSink{name: "speaker"}, &fakeSink{name: "bluetooth"}
	sp.avail.Store(true)
	bt.avail.Store(true)
	rt := newRouter(config.Route{Music: []string{"bluetooth", "speaker"}, Voice: []string{"speaker", "bluetooth"}, Alert: []string{"speaker"}}, sp, bt)
	if !rt.better(engine.Music, "bluetooth") || rt.better(engine.Voice, "bluetooth") || rt.better(engine.Alert, "bluetooth") {
		t.Fatal("музыка — в наушники; голос остаётся на динамиках (они раньше); у звонков наушников нет в маршруте")
	}
	sp.avail.Store(false)
	if !rt.better(engine.Voice, "bluetooth") {
		t.Fatal("динамики недоступны — наушники лучше")
	}
}
