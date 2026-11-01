package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/buttons"
)

type playerStub struct {
	take bool
	got  []control.Cmd
	from []control.Origin
}

func (p *playerStub) Handle(c control.Cmd, from control.Origin) bool {
	p.got = append(p.got, c)
	p.from = append(p.from, from)
	return p.take
}

// newPlayerDaemon — демон с Hub'ом и плеерами, подключёнными так же, как это делает attach.
func newPlayerDaemon(comps ...*component) *daemon {
	d := &daemon{j: &journal.Journal{Cap: 50}, comps: comps}
	d.hub = &control.Hub{OnHandled: d.controlHandled}
	for _, c := range comps {
		if c.err == nil { // как в startComponents: подключается только созданный компонент
			d.attach(c)
		}
	}
	return d
}

// Плеером становится компонент с Handle; сломанный и безинтерфейсный в Hub не попадают, а действие кнопок колонки уходит туда как команда
// «откуда: колонка». Выполнил один плеер — остальные команды не видят.
func TestSpeakerButtonsGoToThePlayersThroughTheHub(t *testing.T) {
	skip, first, second := &playerStub{}, &playerStub{take: true}, &playerStub{take: true}
	d := newPlayerDaemon(
		&component{name: "none", obj: struct{}{}},
		&component{name: "skip", obj: skip},
		&component{name: "first", obj: first},
		&component{name: "second", obj: second},
	)
	if d.hub.Len() != 3 {
		t.Fatalf("плееров в Hub %d, ждали 3", d.hub.Len())
	}
	for a, want := range map[buttons.Action]control.Cmd{buttons.Toggle: control.Toggle, buttons.Next: control.Next, buttons.Prev: control.Prev} {
		first.got, first.from = nil, nil
		d.button(a)
		if len(first.got) != 1 || first.got[0] != want || first.from[0] != control.Speaker {
			t.Fatalf("%s: плеер получил %v от %v, ждали %q от колонки", a, first.got, first.from, want)
		}
	}
	if len(second.got) != 0 {
		t.Fatalf("команда дошла до второго плеера: %v", second.got)
	}
}

// Лог платформы читается, только если есть плеер; нажатие из лога доходит до него командой.
func TestWatchButtonsFollowsThePlatformLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	if err := os.WriteFile(p, []byte("старьё\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := make(chan control.Cmd, 4)
	d := newPlayerDaemon(&component{name: "h", obj: &chanPlayer{ch: got}})
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
		if c != control.Toggle {
			t.Fatalf("получили %q", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нажатие из лога не дошло до плеера")
	}
}

type chanPlayer struct{ ch chan control.Cmd }

func (c *chanPlayer) Handle(cmd control.Cmd, _ control.Origin) bool {
	c.ch <- cmd
	return true
}

func TestWatchButtonsDoesNothingWithoutPlayers(t *testing.T) {
	d := newPlayerDaemon(&component{name: "none", obj: struct{}{}}, &component{name: "broken", obj: &playerStub{take: true}, err: errors.New("не запустился")})
	d.platformLog = filepath.Join(t.TempDir(), "нет")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.watchButtons(ctx) // не должно ни паниковать, ни стартовать горутину, которая живёт вечно
}
