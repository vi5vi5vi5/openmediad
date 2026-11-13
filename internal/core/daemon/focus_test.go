package daemon

import (
	"errors"
	"testing"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
)

func focusCommand(on uint64) msg.AudioCommand { return pb.AppendUint(nil, 39, on) }

func testDaemon(t *testing.T) *daemon {
	t.Helper()
	d := &daemon{cfg: config.Defaults()}
	d.setupJournal()
	d.setupEngine()
	if err := d.setupShim(); err != nil {
		t.Fatal(err)
	}
	return d
}

// Музыку на время диалога приглушает ядро, а не компонент Яндекса: без единого бэкенда музыки (сборка только со своим плеером)
// команда фокуса всё равно меняет громкость класса «музыка», и голос с звонками при этом не трогаются.
func TestFocusDucksMusicWithoutAnyMusicBackend(t *testing.T) {
	d := testDaemon(t)
	d.cfg.Focus.DuckGain = 0.2
	d.m.SetDuck(0.2)
	if !d.m.NoMusic {
		t.Fatal("в тесте не должно быть бэкенда музыки")
	}
	if d.m.Handle(nil, focusCommand(1)) {
		t.Fatal("команду фокуса надо пропустить штатному mediad")
	}
	if g := d.eng.ClassGain(engine.Music); g != 0.2 {
		t.Fatalf("диалог начался: громкость музыки %v, ждали 0.2", g)
	}
	if g := d.eng.ClassGain(engine.Voice); g != 1 {
		t.Fatalf("голос приглушаться не должен: %v", g)
	}
	if g := d.eng.ClassGain(engine.Alert); g != 1 {
		t.Fatalf("звонки приглушаться не должны: %v", g)
	}
	d.m.Handle(nil, focusCommand(0))
	if g := d.eng.ClassGain(engine.Music); g != 1 {
		t.Fatalf("диалог кончился: громкость музыки %v, ждали 1", g)
	}
}

type focusStub struct{ got []bool }

func (f *focusStub) OnFocus(on bool) { f.got = append(f.got, on) }

// О начале и конце диалога узнают компоненты с OnFocus; повтор того же состояния не рассылается, сломанный компонент пропускается.
func TestFocusIsDeliveredToHandlers(t *testing.T) {
	d := testDaemon(t)
	a, broken := &focusStub{}, &focusStub{}
	d.comps = []*component{{name: "a", obj: a}, {name: "none", obj: struct{}{}}, {name: "broken", obj: broken, err: errors.New("x")}}
	for _, on := range []uint64{1, 1, 0} {
		d.m.Handle(nil, focusCommand(on))
	}
	if len(a.got) != 2 || !a.got[0] || a.got[1] {
		t.Fatalf("компонент получил %v, ждали [true false]", a.got)
	}
	if len(broken.got) != 0 {
		t.Fatal("фокус ушёл компоненту, который не запустился")
	}
}
