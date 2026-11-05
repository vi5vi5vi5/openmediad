package control

import (
	"context"
	"testing"
	"time"
)

// step — один шаг уступки звука с тем же состоянием «что было раньше», которое держит Run.
type arbRig struct {
	*rig
	prev  map[string]bool
	prevY bool
}

func newArbRig(st State) *arbRig {
	return &arbRig{rig: newRig(st), prev: map[string]bool{}}
}

func (a *arbRig) step() { a.prevY = a.h.arbitrate(a.prev, a.prevY) }

// Заиграл плеер движка, пока играл Яндекс: Яндекс остановлен, «продолжи» теперь за плеером.
func TestPlayerStartingTakesTheSoundFromYandex(t *testing.T) {
	a := newArbRig(State{Ready: true})
	a.plat.setYandex(true)
	a.step() // Яндекс играл раньше
	if a.h.Owner() != OwnerPlatform {
		t.Fatalf("хозяин %q, ждали Яндекс", a.h.Owner())
	}
	a.p.set(ready(true))
	a.step()
	if a.plat.takeovers != 1 || a.h.Owner() != "spotify" || a.p.yielded != 1 {
		t.Fatalf("TakeOver %d, хозяин %q, уступок %d", a.plat.takeovers, a.h.Owner(), a.p.yielded)
	}
	if a.p.count() != 0 {
		t.Fatal("плееру, который заиграл, велели паузу")
	}
	a.step()
	if a.plat.takeovers != 1 {
		t.Fatal("уступка повторилась, хотя ничего не изменилось")
	}
}

// Заиграл Яндекс, пока звучал плеер движка: плеер на паузе, «продолжи» теперь за Яндексом.
func TestYandexStartingPausesThePlayer(t *testing.T) {
	a := newArbRig(ready(true))
	a.plat.stopped = 0 // Яндекс не играл: останавливать у платформы нечего
	a.step()           // плеер заиграл первым
	if a.h.Owner() != "spotify" {
		t.Fatalf("хозяин %q", a.h.Owner())
	}
	a.plat.setYandex(true)
	a.step()
	if c, ok := a.p.last(); !ok || c != Pause || a.p.froms[len(a.p.froms)-1] != Arbiter {
		t.Fatalf("плееру велели %v от %v", a.p.did, a.p.froms)
	}
	if a.h.Owner() != OwnerPlatform || a.p.yielded != 1 {
		t.Fatalf("хозяин %q, уступок %d", a.h.Owner(), a.p.yielded)
	}
}

// Яндекс уже играл, плеер заиграл в тот же шаг: побеждает плеер движка.
func TestSimultaneousStartGoesToThePlayer(t *testing.T) {
	a := newArbRig(ready(true))
	a.plat.setYandex(true)
	a.step()
	if a.h.Owner() != "spotify" || a.plat.takeovers != 1 {
		t.Fatalf("хозяин %q, TakeOver %d", a.h.Owner(), a.plat.takeovers)
	}
	if a.p.count() != 0 {
		t.Fatal("плеер поставлен на паузу в пользу Яндекса, который сам только что остановлен")
	}
}

// Заиграл плеер, а у платформы нечего останавливать: уступки нет, и в статус она не идёт.
func TestNothingToTakeOverMeansNoYield(t *testing.T) {
	a := newArbRig(ready(true))
	a.plat.stopped = 0
	a.step()
	if a.p.yielded != 0 {
		t.Fatal("уступка засчитана, хотя Яндекс не играл")
	}
	if a.h.Owner() != "spotify" {
		t.Fatalf("хозяин %q", a.h.Owner())
	}
}

// Пауза из приложения и новый старт: «продолжи» всегда за последним, кто заиграл.
func TestOwnerFollowsTheLastToStart(t *testing.T) {
	a := newArbRig(ready(true))
	a.step()
	a.p.set(paused())
	a.step()
	a.plat.setYandex(true)
	a.step()
	if a.h.Owner() != OwnerPlatform {
		t.Fatalf("хозяин %q, ждали Яндекс", a.h.Owner())
	}
	a.plat.setYandex(false)
	a.p.set(ready(true))
	a.step()
	if a.h.Owner() != "spotify" {
		t.Fatalf("хозяин %q, ждали плеер", a.h.Owner())
	}
}

// Два плеера движка: заигравший второй останавливает первого.
func TestSecondPlayerPausesTheFirst(t *testing.T) {
	a := newArbRig(ready(true))
	second := &fakePlayer{st: State{Ready: true}}
	a.h.Add("other", second)
	a.step()
	second.set(ready(true))
	a.step()
	if c, ok := a.p.last(); !ok || c != Pause {
		t.Fatalf("первому плееру велели %v", a.p.did)
	}
	if second.count() != 0 || a.h.Owner() != "other" {
		t.Fatalf("второму велели %v, хозяин %q", second.did, a.h.Owner())
	}
}

// Run работает до отмены контекста и сам уступает звук.
func TestRunArbitratesUntilCancelled(t *testing.T) {
	done0, stop0 := context.WithCancel(context.Background())
	stop0()
	(&Hub{}).Run(done0) // отменённый контекст — сразу возврат, и без платформы Run не падает
	r := newRig(State{Ready: true})
	r.plat.setYandex(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.h.Run(ctx); close(done) }()
	time.Sleep(arbitrateEvery + 100*time.Millisecond)
	r.p.set(ready(true))
	deadline := time.Now().Add(3 * time.Second)
	for r.h.Owner() != "spotify" {
		if time.Now().After(deadline) {
			t.Fatal("Run не заметил, что заиграл плеер")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился")
	}
}
