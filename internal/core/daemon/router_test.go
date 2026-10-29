package daemon

import (
	"errors"
	"sync/atomic"
	"testing"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
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
	rt := newRouter(config.Route{Music: []string{"headphones", "speaker"}, Voice: []string{"speaker"}, Alert: []string{"speaker"}}, sp)
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
	bt := &fakeSink{name: "headphones"}
	rt.add(bt)
	if out(engine.Music) != "speaker" {
		t.Fatal("наушники не подключены — музыка на динамиках")
	}
	bt.avail.Store(true)
	if out(engine.Music) != "headphones" || out(engine.Voice) != "speaker" {
		t.Fatal("музыка — в наушники, голос — на динамики")
	}
	bt.fail = true
	if out(engine.Music) != "speaker" {
		t.Fatal("наушники не открылись — следующий выход маршрута")
	}
}

// Подключились наушники: переводить в них стоит только классы, у которых они в маршруте раньше доступного выхода.
func TestRouterBetter(t *testing.T) {
	sp, bt := &fakeSink{name: "speaker"}, &fakeSink{name: "headphones"}
	sp.avail.Store(true)
	bt.avail.Store(true)
	rt := newRouter(config.Route{Music: []string{"headphones", "speaker"}, Voice: []string{"speaker", "headphones"}, Alert: []string{"speaker"}}, sp, bt)
	if !rt.better(engine.Music, "headphones") || rt.better(engine.Voice, "headphones") || rt.better(engine.Alert, "headphones") {
		t.Fatal("музыка — в наушники; голос остаётся на динамиках (они раньше); у звонков наушников нет в маршруте")
	}
	sp.avail.Store(false)
	if !rt.better(engine.Voice, "headphones") {
		t.Fatal("динамики недоступны — наушники лучше")
	}
}
