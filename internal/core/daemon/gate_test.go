package daemon

import (
	"sync"
	"testing"
	"time"
)

type recVoice struct {
	mu    sync.Mutex
	calls []string
}

func (r *recVoice) add(s string)              { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }
func (r *recVoice) Start(id string)           { r.add("start " + id) }
func (r *recVoice) Chunk(id string, b []byte) { r.add("chunk " + id) }
func (r *recVoice) EOS(id string)             { r.add("eos " + id) }
func (r *recVoice) Abort(id string)           { r.add("abort " + id) }
func (r *recVoice) Pos(id string) uint64      { return 77 }
func (r *recVoice) VoicePlaying(id string)    { r.add("playing " + id) }
func (r *recVoice) VoiceEnded(id string)      { r.add("ended " + id) }
func (r *recVoice) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func TestGateIsTransparentByDefault(t *testing.T) {
	inner := &recVoice{}
	g := newVoiceGate(inner, &recVoice{})
	g.Start("a")
	g.Chunk("a", []byte{1})
	g.EOS("a")
	if got := inner.list(); len(got) != 3 || got[0] != "start a" || got[2] != "eos a" {
		t.Fatalf("прозрачный шлюз: %v", got)
	}
	if g.Pos("a") != 77 {
		t.Fatal("позиция не из бэкенда")
	}
}

func TestGateSwallowsOneAnswerAndReportsItPlayed(t *testing.T) {
	inner, ev := &recVoice{}, &recVoice{}
	g := newVoiceGate(inner, ev)
	g.Swallow()
	g.Start("a")
	g.Chunk("a", []byte{1})
	g.Chunk("a", []byte{2})
	g.EOS("a")
	deadline := time.Now().Add(time.Second)
	for len(ev.list()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(inner.list()) != 0 {
		t.Fatalf("погашенный ответ дошёл до бэкенда: %v", inner.list())
	}
	if got := ev.list(); len(got) != 2 || got[0] != "playing a" || got[1] != "ended a" {
		t.Fatalf("события для платформы: %v (нужно: сначала playing, потом ended)", got)
	}
	if g.Pos("a") != 0 || g.Swallowed() != 1 {
		t.Fatal("позиция погашенного ответа должна быть 0, а счёт — 1")
	}
	// гасился ровно один ответ: следующий звучит
	g.Start("b")
	g.Chunk("b", []byte{3})
	if got := inner.list(); len(got) != 2 || got[0] != "start b" {
		t.Fatalf("следующий ответ не дошёл до бэкенда: %v", got)
	}
	g.Abort("a") // Abort погашенного не трогает бэкенд
	if got := inner.list(); len(got) != 2 {
		t.Fatalf("Abort погашенного дошёл до бэкенда: %v", got)
	}
}

func TestGateSwallowExpires(t *testing.T) {
	inner := &recVoice{}
	g := newVoiceGate(inner, &recVoice{})
	now := time.Unix(1000, 0)
	g.now = func() time.Time { return now }
	g.Swallow()
	now = now.Add(swallowWindow + time.Second)
	g.Start("late")
	if got := inner.list(); len(got) != 1 || got[0] != "start late" {
		t.Fatalf("ответ после окна должен звучать: %v", got)
	}
	if g.Swallowed() != 0 {
		t.Fatal("устаревшее окно погасило ответ")
	}
}

func TestGateClearDropsPendingSwallow(t *testing.T) {
	inner := &recVoice{}
	g := newVoiceGate(inner, &recVoice{})
	g.Swallow()
	g.Clear() // новая фраза, которую никто не перехватил
	g.Start("a")
	if got := inner.list(); len(got) != 1 || got[0] != "start a" {
		t.Fatalf("после Clear ответ должен звучать: %v", got)
	}
	if g.Swallowed() != 0 {
		t.Fatal("после Clear ответ погашен")
	}
}
