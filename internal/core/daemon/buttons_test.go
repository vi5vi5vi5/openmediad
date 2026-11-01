package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/buttons"
)

type buttonStub struct {
	take bool
	got  []buttons.Action
}

func (b *buttonStub) OnButton(a buttons.Action) bool {
	b.got = append(b.got, a)
	return b.take
}

func newButtonDaemon(comps ...*component) *daemon {
	return &daemon{j: &journal.Journal{Cap: 50}, comps: comps}
}

// Действие кнопок получает первый компонент, который его выполнил; остальные его уже не видят. Сломанные и безынтерфейсные пропускаются.
func TestButtonStopsAtTheFirstComponentThatHandledIt(t *testing.T) {
	skip, first, second, broken := &buttonStub{}, &buttonStub{take: true}, &buttonStub{take: true}, &buttonStub{take: true}
	d := newButtonDaemon(
		&component{name: "none", obj: struct{}{}},
		&component{name: "broken", obj: broken, err: errors.New("не запустился")},
		&component{name: "skip", obj: skip},
		&component{name: "first", obj: first},
		&component{name: "second", obj: second},
	)
	d.button(buttons.Next)
	if len(skip.got) != 1 || len(first.got) != 1 || first.got[0] != buttons.Next {
		t.Fatalf("skip %v, first %v", skip.got, first.got)
	}
	if len(second.got) != 0 || len(broken.got) != 0 {
		t.Fatalf("второй %v, сломанный %v: действие дошло дальше выполнившего", second.got, broken.got)
	}
}

// Лог платформы читается, только если есть компонент, которому нужны кнопки; нажатие из лога доходит до него.
func TestWatchButtonsFollowsThePlatformLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	if err := os.WriteFile(p, []byte("старьё\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &buttonStub{take: true}
	got := make(chan buttons.Action, 4)
	d := newButtonDaemon(&component{name: "h", obj: &chanButton{buttonStub: h, ch: got}})
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
	case a := <-got:
		if a != buttons.Toggle {
			t.Fatalf("получили %q", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нажатие из лога не дошло до компонента")
	}
}

type chanButton struct {
	*buttonStub
	ch chan buttons.Action
}

func (c *chanButton) OnButton(a buttons.Action) bool {
	c.ch <- a
	return true
}

func TestWatchButtonsDoesNothingWithoutHandlers(t *testing.T) {
	d := newButtonDaemon(&component{name: "none", obj: struct{}{}})
	d.platformLog = filepath.Join(t.TempDir(), "нет")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.watchButtons(ctx) // не должно ни паниковать, ни стартовать горутину, которая живёт вечно
}
