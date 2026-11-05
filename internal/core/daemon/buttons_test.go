package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/buttons"
)

// newPlayerDaemon — демон с Hub'ом и плеерами, подключёнными так же, как это делает attach.
func newPlayerDaemon(comps ...*component) *daemon {
	d := &daemon{j: &journal.Journal{Cap: 50}, comps: comps}
	d.hub = &control.Hub{OnHandled: d.controlHandled, Platform: &controltest.Platform{}}
	for _, c := range comps {
		if c.err == nil { // как в startComponents: подключается только созданный компонент
			d.attach(c)
		}
	}
	return d
}

// yandexPlays включает музыку Яндекса в поддельной платформе демона.
func yandexPlays(d *daemon) { d.hub.Platform.(*controltest.Platform).SetPlaying(true) }

// Плеером становится компонент с Accepts/State/Do; сломанный и безинтерфейсный в Hub не попадают. Действие кнопок колонки уходит туда
// командой «откуда: колонка»; выполнил один плеер — остальные команды не видят.
func TestSpeakerButtonsGoToThePlayersThroughTheHub(t *testing.T) {
	idle, first, second := controltest.NewPlayer(control.State{}), controltest.NewPlayer(controltest.Playing()), controltest.NewPlayer(controltest.Playing())
	d := newPlayerDaemon(
		&component{name: "none", obj: struct{}{}},
		&component{name: "broken", obj: controltest.NewPlayer(controltest.Playing()), err: errors.New("не запустился")},
		&component{name: "idle", obj: idle},
		&component{name: "first", obj: first},
		&component{name: "second", obj: second},
	)
	if d.hub.Len() != 3 {
		t.Fatalf("плееров в Hub %d, ждали 3", d.hub.Len())
	}
	// кнопка при играющем плеере — пауза; жесты — дальше и назад
	for a, want := range map[buttons.Action]control.Cmd{buttons.Toggle: control.Pause, buttons.Next: control.Next, buttons.Prev: control.Prev} {
		before := first.Count()
		d.button(a)
		done := first.Done()
		if len(done) != before+1 || done[before] != (controltest.Done{Cmd: want, From: control.Speaker}) {
			t.Fatalf("%s: плеер получил %v, ждали %q от колонки", a, done[before:], want)
		}
	}
	if second.Count() != 0 || idle.Count() != 0 {
		t.Fatalf("команда дошла не тому: второй %v, молчащий %v", second.Done(), idle.Done())
	}
}

// Лог платформы читается, только если есть плеер; нажатие из лога доходит до него командой.
func TestWatchButtonsFollowsThePlatformLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	if err := os.WriteFile(p, []byte("старьё\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := make(chan control.Cmd, 4)
	pl := controltest.NewPlayer(controltest.Playing())
	pl.OnDo = func(c control.Cmd, _ control.Origin) { got <- c }
	d := newPlayerDaemon(&component{name: "h", obj: pl})
	d.platformLog = p
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.watchButtons(ctx)
	time.Sleep(120 * time.Millisecond) // первый опрос уже сделан: старое не повторится
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[1] [t] [info] [buttons_actions.cpp:298] Toggle play/pause\n")
	f.Close()
	select {
	case c := <-got:
		if c != control.Pause {
			t.Fatalf("получили %q", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нажатие из лога не дошло до плеера")
	}
}

func TestWatchButtonsDoesNothingWithoutPlayers(t *testing.T) {
	d := newPlayerDaemon(&component{name: "none", obj: struct{}{}}, &component{name: "broken", obj: controltest.NewPlayer(controltest.Playing()), err: errors.New("не запустился")})
	d.platformLog = filepath.Join(t.TempDir(), "нет")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.watchButtons(ctx) // не должно ни паниковать, ни стартовать горутину, которая живёт вечно
}
