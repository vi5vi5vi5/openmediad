package engine

import (
	"testing"
	"time"

	"openmediad/internal/core/audio"
)

// tailOut — вывод с хвостом: Drain возвращается сразу, а звук ещё звучит tail (как наушники).
type tailOut struct {
	memOut
	tail time.Duration
}

func (o *tailOut) Tail() time.Duration { return o.tail }

func tailRig(t *testing.T, tail time.Duration) *rig {
	r := newRig(t)
	r.e.Router = RouterFunc(func(Class, audio.Format) (audio.Output, error) { return &tailOut{tail: tail}, nil })
	return r
}

// Поток с WaitTail сообщает о конце, только когда хвост вывода отзвучал; без WaitTail — сразу, как раньше.
func TestEndedWaitsForOutputTail(t *testing.T) {
	const tail = 150 * time.Millisecond
	for _, wait := range []bool{false, true} {
		r := tailRig(t, tail)
		src := &memSource{f: audio.Mono48, data: make([]byte, 4800)}
		st := r.e.NewStream("v", Voice, src, Options{Spectrum: NoSpectrum, WaitTail: wait}, r.events4("v"))
		start := time.Now()
		st.Play()
		for ev := r.next(t); ev != "ended v"; ev = r.next(t) {
		}
		got := time.Since(start)
		if wait && got < tail {
			t.Fatalf("WaitTail: конец объявлен через %v, раньше хвоста %v", got, tail)
		}
		if !wait && got >= tail {
			t.Fatalf("без WaitTail конец не должен ждать хвост: %v", got)
		}
		st.Close()
	}
}

// Закрытие потока, пока звучит хвост, не оставляет события «кончился»: после Close событий не бывает.
func TestCloseDuringTailSendsNothing(t *testing.T) {
	r := tailRig(t, time.Second)
	src := &memSource{f: audio.Mono48, data: make([]byte, 4800)}
	st := r.e.NewStream("v", Voice, src, Options{Spectrum: NoSpectrum, WaitTail: true}, r.events4("v"))
	st.Play()
	for ev := r.next(t); ev != "playing v"; ev = r.next(t) {
	}
	time.Sleep(50 * time.Millisecond) // звук отдан, идёт ожидание хвоста
	done := make(chan struct{})
	go func() { st.CloseWait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close должен прервать ожидание хвоста")
	}
	select {
	case ev := <-r.events:
		t.Fatalf("после Close пришло событие %q", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Предел: неправдоподобный хвост не держит поток дольше maxTail.
func TestTailIsCapped(t *testing.T) {
	r := tailRig(t, time.Hour)
	st := r.e.NewStream("v", Voice, &memSource{f: audio.Mono48, data: make([]byte, 480)}, Options{Spectrum: NoSpectrum, WaitTail: true}, r.events4("v"))
	st.Play()
	deadline := time.After(maxTail + time.Second)
	for {
		select {
		case ev := <-r.events:
			if ev == "ended v" {
				st.Close()
				return
			}
		case <-deadline:
			t.Fatal("хвост не ограничен")
		}
	}
}
