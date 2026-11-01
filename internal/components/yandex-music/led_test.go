package yandexmusic

import (
	"sync"
	"testing"
	"time"

	"openmediad/internal/core/led"
)

// recLED запоминает, что модуль сообщил панели индикации.
type recLED struct {
	mu  sync.Mutex
	log []string
}

func (r *recLED) On(role led.Role, name string, def led.Color) {
	r.mu.Lock()
	r.log = append(r.log, "on "+name+" "+def.Hex())
	r.mu.Unlock()
}

func (r *recLED) Off(role led.Role, name string) {
	r.mu.Lock()
	r.log = append(r.log, "off "+name)
	r.mu.Unlock()
}

func (r *recLED) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func TestMarkLightsWhileAnyPlayerSounds(t *testing.T) {
	r := &recLED{}
	b := &Backend{LED: r}
	p1, p2 := &player{}, &player{}
	b.mark(p1, true)
	b.mark(p2, true)
	b.mark(p1, true) // повторное включение того же плеера ничего не меняет
	b.mark(p1, false)
	if got := r.get(); len(got) != 1 || got[0] != "on yandex-music #ffb000" {
		t.Fatalf("пока звучит второй плеер, подсветка не должна гаснуть: %v", got)
	}
	b.mark(p2, false)
	b.mark(p2, false)
	if got := r.get(); len(got) != 2 || got[1] != "off yandex-music" {
		t.Fatalf("после последнего плеера подсветка гаснет один раз: %v", got)
	}
	(&Backend{}).mark(p1, true) // без панели — ничего, без паники
}

// Через настоящий путь: Start зажигает, Pause гасит, Close гасит.
func TestBackendReportsPlaybackToTheLED(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond
	r := &recLED{}
	h.b.LED = r
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	time.Sleep(40 * time.Millisecond)
	if got := r.get(); len(got) != 1 || got[0][:2] != "on" {
		t.Fatalf("после Start: %v", got)
	}
	h.b.Pause("p1")
	if got := r.get(); len(got) != 2 || got[1] != "off yandex-music" {
		t.Fatalf("после Pause: %v", got)
	}
	h.b.Start("p1")
	h.b.Close("p1")
	if got := r.get(); len(got) != 4 || got[3] != "off yandex-music" {
		t.Fatalf("после повторного Start и Close: %v", got)
	}
}
