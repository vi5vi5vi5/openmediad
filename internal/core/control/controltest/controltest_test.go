package controltest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/control"
)

// Поддельный плеер сам выполняет договор: на нём строятся тесты других пакетов.
func TestFakePlayerFollowsTheContract(t *testing.T) {
	CheckPlayer(t, NewPlayer(Playing()))
	CheckPlayer(t, NewPlayer(control.State{}))
}

func TestFakePlayerRecordsAndRefuses(t *testing.T) {
	p := NewPlayer(Playing())
	var seen []control.Cmd
	p.OnDo = func(c control.Cmd, _ control.Origin) { seen = append(seen, c) }
	if err := p.Do(control.Next, control.Headphones); err != nil {
		t.Fatal(err)
	}
	if got := p.Done(); len(got) != 1 || got[0] != (Done{control.Next, control.Headphones}) || len(seen) != 1 {
		t.Fatalf("записано %v, OnDo %v", got, seen)
	}
	p.Fail(errors.New("нет связи"))
	if p.Do(control.Pause, control.Voice) == nil || p.Count() != 1 {
		t.Fatal("при сбое Do должен возвращать ошибку и ничего не записывать")
	}
	p.Fail(nil)
	p.Refuse(control.Voice, control.Cloud)
	if p.Accepts(control.Voice) || !p.Accepts(control.Speaker) {
		t.Fatal("отказ источникам не применился")
	}
	p.Refuse()
	if !p.Accepts(control.Voice) {
		t.Fatal("отказ не снялся")
	}
	p.Set(Paused())
	if !p.State().Resumable {
		t.Fatal("состояние не поменялось")
	}
}

func TestFakePlatformMimicsTheAutomaton(t *testing.T) {
	f := &Platform{Stopped: 2}
	f.SetPlaying(true)
	f.ArmCarrier(time.Second)
	if !f.Playing() || f.Armed() != 1 {
		t.Fatal("Яндекс или счётчик очереди")
	}
	if f.TakeOver() != 2 || f.Playing() || f.TakeOvers() != 1 {
		t.Fatal("TakeOver должен остановить Яндекс и сказать, сколько")
	}
	var _ control.Platform = f
}

// ---- плохие плееры: каждое нарушение договора находится ----

type goodBase struct{ st control.State }

func (goodBase) Accepts(control.Origin) bool { return true }
func (g goodBase) State() control.State      { return g.st }
func (goodBase) Do(c control.Cmd, _ control.Origin) error {
	switch c {
	case control.Pause, control.Resume, control.Next, control.Prev:
		return nil
	}
	return errors.New("нет такой команды")
}

type acceptsFlipFlop struct {
	goodBase
	n int
}

func (a *acceptsFlipFlop) Accepts(control.Origin) bool { a.n++; return a.n%2 == 0 }

type acceptsPanics struct{ goodBase }

func (acceptsPanics) Accepts(control.Origin) bool { panic("нет настроек") }

type stateContradicts struct{ goodBase }

func (stateContradicts) State() control.State { return control.State{Ready: true, Sounding: true} }

type stateBothPlayingAndPaused struct{ goodBase }

func (stateBothPlayingAndPaused) State() control.State {
	return control.State{Ready: true, Playing: true, Paused: true}
}

type stateBlocks struct {
	goodBase
	release chan struct{}
}

func (s stateBlocks) State() control.State { <-s.release; return control.State{} }

type doTakesToggle struct{ goodBase }

func (doTakesToggle) Do(control.Cmd, control.Origin) error { return nil }

type doBlocks struct {
	goodBase
	release chan struct{}
}

func (d doBlocks) Do(c control.Cmd, _ control.Origin) error {
	if c == control.Pause {
		<-d.release
	}
	return errors.New("нет такой команды")
}

// deadlineForTests укорачивает срок ответа плеера; возвращает функцию, которая его возвращает.
func deadlineForTests(d time.Duration) func() {
	old := deadline
	deadline = d
	return func() { deadline = old }
}

func TestViolationsAreFound(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	old := deadlineForTests(300 * time.Millisecond)
	defer old()
	for name, tc := range map[string]struct {
		p    control.Player
		want string
	}{
		"Accepts меняет ответ": {&acceptsFlipFlop{}, "по-разному"},
		"Accepts паникует":     {acceptsPanics{}, "паника"},
		"Sounding без Playing": {stateContradicts{}, "Sounding без Playing"},
		"играет и на паузе":    {stateBothPlayingAndPaused{}, "на паузе одновременно"},
		"State зависает":       {stateBlocks{release: release}, "не ответил"},
		"Do принимает Toggle":  {doTakesToggle{}, "Toggle"},
		"Do зависает":          {doBlocks{release: release}, "не ответил"},
	} {
		t.Run(name, func(t *testing.T) {
			got := Violations(tc.p)
			if len(got) == 0 || !strings.Contains(strings.Join(got, "; "), tc.want) {
				t.Fatalf("нарушения %v, ждали упоминание %q", got, tc.want)
			}
		})
	}
	if v := Violations(goodBase{st: Playing()}); len(v) != 0 {
		t.Fatalf("хороший плеер признан плохим: %v", v)
	}
}
