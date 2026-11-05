// Package controltest — заготовки для тестов плееров движка и их источников: поддельные плеер и платформа и проверка договора плеера.
//
// Плеер пишет тот, кому нужен ещё один источник музыки (см. docs/PLAYERS.md). Чтобы не придумывать для тестов свои подделки и не забыть краевой
// случай, здесь есть всё готовое: Player — плеер с состоянием из теста, Platform — музыка Яндекса и автомат mediad, CheckPlayer — договор, который
// выполняет любой хороший плеер.
package controltest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"openmediad/internal/core/control"
)

// Playing — состояние «плеер играет».
func Playing() control.State {
	return control.State{Ready: true, Playing: true, Sounding: true}
}

// Paused — состояние «плеер на паузе, сессия его».
func Paused() control.State {
	return control.State{Ready: true, Paused: true, Resumable: true}
}

// Done — команда, которую выполнил поддельный плеер.
type Done struct {
	Cmd  control.Cmd
	From control.Origin
}

// Player — поддельный плеер движка: состояние задаёт тест, команды записываются.
type Player struct {
	// OnDo (необязательный) вызывается после записи каждой выполненной команды.
	OnDo func(control.Cmd, control.Origin)

	mu      sync.Mutex
	st      control.State
	refuse  map[control.Origin]bool
	err     error
	done    []Done
	yielded int
}

// NewPlayer — плеер в состоянии st.
func NewPlayer(st control.State) *Player { return &Player{st: st} }

func (p *Player) Accepts(o control.Origin) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.refuse[o]
}

func (p *Player) State() control.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *Player) Do(c control.Cmd, from control.Origin) error {
	p.mu.Lock()
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return err
	}
	switch c {
	case control.Pause, control.Resume, control.Next, control.Prev:
	default:
		p.mu.Unlock()
		return fmt.Errorf("команда %q плееру не передаётся", c)
	}
	p.done = append(p.done, Done{c, from})
	f := p.OnDo
	p.mu.Unlock()
	if f != nil {
		f(c, from)
	}
	return nil
}

// Yielded считает уступки звука (control.Yielder).
func (p *Player) Yielded() { p.mu.Lock(); p.yielded++; p.mu.Unlock() }

// Set меняет состояние плеера.
func (p *Player) Set(st control.State) { p.mu.Lock(); p.st = st; p.mu.Unlock() }

// Refuse делает плеер глухим к источникам (настройки компонента: голос выключен …); без аргументов снимает отказ.
func (p *Player) Refuse(origins ...control.Origin) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuse = map[control.Origin]bool{}
	for _, o := range origins {
		p.refuse[o] = true
	}
}

// Fail заставляет Do возвращать ошибку (нет связи); nil — снова работает.
func (p *Player) Fail(err error) { p.mu.Lock(); p.err = err; p.mu.Unlock() }

// Done — что плееру велели, по порядку.
func (p *Player) Done() []Done {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Done(nil), p.done...)
}

// Count — сколько команд выполнено.
func (p *Player) Count() int { return len(p.Done()) }

// Yields — сколько раз сообщили об уступке звука.
func (p *Player) Yields() int { p.mu.Lock(); defer p.mu.Unlock(); return p.yielded }

// Platform — поддельная музыка Яндекса и автомат mediad (control.Platform).
type Platform struct {
	// Stopped — что вернёт TakeOver: сколько плееров Яндекса остановлено.
	Stopped int

	mu        sync.Mutex
	yandex    bool
	armed     int
	takeovers int
}

// SetPlaying включает и выключает музыку Яндекса.
func (f *Platform) SetPlaying(v bool) { f.mu.Lock(); f.yandex = v; f.mu.Unlock() }

func (f *Platform) Playing() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.yandex }

func (f *Platform) ArmCarrier(time.Duration) { f.mu.Lock(); f.armed++; f.mu.Unlock() }

// TakeOver останавливает Яндекс, как настоящий автомат.
func (f *Platform) TakeOver() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takeovers++
	f.yandex = false
	return f.Stopped
}

// Armed — сколько раз просили отдать плееру ближайший запуск очереди Яндекса.
func (f *Platform) Armed() int { f.mu.Lock(); defer f.mu.Unlock(); return f.armed }

// TakeOvers — сколько раз забирали место у Яндекса.
func (f *Platform) TakeOvers() int { f.mu.Lock(); defer f.mu.Unlock(); return f.takeovers }

// deadline — сколько каждой проверке договора разрешено думать: плеер, который зависает в State или Do, ломает всех.
var deadline = 3 * time.Second

// Violations проверяет договор control.Player и возвращает нарушения (пусто — плеер хороший). Плеер должен быть подключён к подделкам:
// проверка вызывает Do с настоящей командой.
//
//   - Accepts не паникует и отвечает на один источник всегда одинаково, в том числе на незнакомый.
//   - State быстрый, не паникует, безопасен при параллельных вызовах и не противоречит себе (звучит — значит играет; играет и на паузе не бывает).
//   - Do не принимает Toggle (его сводит к паузе или продолжению ядро) и незнакомую команду: ошибка, а не молчаливый успех.
//   - Do не зависает: команда уходит без ожидания результата.
func Violations(p control.Player) []string {
	var out []string
	var mu sync.Mutex
	add := func(s string) { mu.Lock(); out = append(out, s); mu.Unlock() }
	run := func(name string, f func() string) {
		done := make(chan string, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- fmt.Sprintf("паника: %v", r)
				}
			}()
			done <- f()
		}()
		select {
		case s := <-done:
			if s != "" {
				add(name + ": " + s)
			}
		case <-time.After(deadline):
			add(name + ": не ответил за " + deadline.String())
		}
	}

	origins := []control.Origin{control.Speaker, control.Headphones, control.Voice, control.FastVoice, control.Cloud, control.Virtual, control.Arbiter, control.App, "незнакомый"}
	run("Accepts", func() string {
		for _, o := range origins {
			if a, b := p.Accepts(o), p.Accepts(o); a != b {
				return fmt.Sprintf("на источник %q отвечает по-разному", o)
			}
		}
		return ""
	})
	run("State", func() string {
		var wg sync.WaitGroup
		bad := make(chan string, 8)
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						bad <- fmt.Sprintf("паника при параллельных вызовах: %v", r)
					}
				}()
				for i := 0; i < 100; i++ {
					st := p.State()
					if st.Sounding && !st.Playing {
						bad <- "звучит, но не играет (Sounding без Playing)"
						return
					}
					if st.Playing && st.Paused {
						bad <- "играет и на паузе одновременно"
						return
					}
				}
			}()
		}
		wg.Wait()
		close(bad)
		return <-bad
	})
	run("Do(Toggle)", func() string {
		if err := p.Do(control.Toggle, control.Headphones); err == nil {
			return "принял Toggle: ядро сводит его к паузе или продолжению само"
		}
		return ""
	})
	run("Do(незнакомая команда)", func() string {
		if err := p.Do("bogus", control.Headphones); err == nil {
			return "принял незнакомую команду молча"
		}
		return ""
	})
	run("Do(Pause)", func() string {
		p.Do(control.Pause, control.Headphones) // ошибка допустима (нет связи), зависание нет
		return ""
	})
	return out
}

// CheckPlayer — Violations как тест: каждое нарушение договора ошибка теста.
func CheckPlayer(t *testing.T, p control.Player) {
	t.Helper()
	for _, v := range Violations(p) {
		t.Errorf("плеер нарушает договор: %s", v)
	}
}
