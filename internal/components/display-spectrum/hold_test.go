package displayspectrum

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/spectrum"
)

// holdRig — отправитель с подставным iohub; тест подаёт события и смотрит, что ушло платформе.
type holdRig struct {
	s *Sender
	f *fakeIohub
}

func newHoldRig(t *testing.T) *holdRig { return newHoldRigDelay(t, 20*time.Millisecond) }

func newHoldRigDelay(t *testing.T, delay time.Duration) *holdRig {
	t.Helper()
	f := &fakeIohub{}
	s := NewSender(f, delay, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	return &holdRig{s, f}
}

// settle ждёт, пока уйдёт всё заказанное, и называет сообщения по порядку: start7, stop7, resume7 (номер — поток), data.
func (r *holdRig) settle() []string {
	time.Sleep(120 * time.Millisecond)
	names := map[int]string{KindStart: "start", KindStop: "stop", KindResume: "resume"}
	var out []string
	for _, m := range r.f.got() {
		name := "data"
		for kind, n := range names {
			for _, stream := range []uint64{7, 8} {
				if bytes.Equal(m.msg, Control(uint64(kind), stream, SourceMusic)) {
					name = n + string(rune('0'+stream))
				}
			}
		}
		out = append(out, name)
	}
	return out
}

func (r *holdRig) frame(stream uint64) {
	r.s.Add(spectrum.Event{Kind: KindData, Source: SourceMusic, Stream: stream, DB: make([]float32, 513), At: time.Now()})
}

func (r *holdRig) ev(kind int, stream uint64) {
	r.s.Add(spectrum.Event{Kind: kind, Source: SourceMusic, Stream: stream, At: time.Now()})
}

func same(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// Музыка играет и рисует спектр на дисплее: удержание останавливает его сразу, кадры не идут, а когда удержание снято, поток продолжается.
func TestHoldStopsTheSpectrumAndResumesIt(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	r.frame(7)
	release := r.s.Hold()
	r.frame(7) // во время удержания кадры не нужны
	r.frame(7)
	release()
	r.frame(7)
	if got, want := r.settle(), []string{"start7", "data", "stop7", "resume7", "data"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}

// Перемотка: пока держим, старый трек кончился, начался новый. Платформа новый поток видит только после удержания, и первым делом «старт», а не «продолжение».
func TestHoldDefersTheNextTrackStream(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	release := r.s.Hold()
	r.ev(KindStop, 7) // платформа поток уже остановила: вторая «остановка» не нужна
	r.ev(KindStart, 8)
	r.frame(8)
	if got := r.settle(); !same(got, []string{"start7", "stop7"}) {
		t.Fatalf("во время удержания ушло %v", got)
	}
	release()
	r.frame(8)
	if got, want := r.settle(), []string{"start7", "stop7", "start8", "data"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}

// Пауза: музыка остановилась, пока держали. Возвращать нечего.
func TestHoldDoesNotResumeAStoppedStream(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	release := r.s.Hold()
	r.ev(KindStop, 7)
	release()
	if got, want := r.settle(), []string{"start7", "stop7"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}

// Спектр не рисуется (музыка на паузе): удержание ничего не отправляет и не мешает следующему потоку после себя.
func TestHoldWithoutAStreamSendsNothing(t *testing.T) {
	r := newHoldRig(t)
	release := r.s.Hold()
	release()
	r.ev(KindStart, 7)
	if got, want := r.settle(), []string{"start7"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}

// Удержаний несколько (быстро нажали перемотку несколько раз): спектр возвращается, когда сняты все, и один раз.
func TestHoldsStack(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	a, b := r.s.Hold(), r.s.Hold()
	a()
	a() // повторное снятие того же удержания ничего не значит
	if got := r.settle(); !same(got, []string{"start7", "stop7"}) {
		t.Fatalf("одно удержание ещё держит, а ушло %v", got)
	}
	b()
	if got, want := r.settle(), []string{"start7", "stop7", "resume7"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}

// Забытое удержание снимается само: дисплей не должен остаться без спектра насовсем.
func TestHoldIsReleasedByTheTimeout(t *testing.T) {
	defer func(old time.Duration) { maxHold = old }(maxHold)
	maxHold = 60 * time.Millisecond
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	late := r.s.Hold()
	if got, want := r.settle(), []string{"start7", "stop7", "resume7"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
	maxHold = time.Second
	next := r.s.Hold()
	late() // опоздавшее снятие не должно снять чужое удержание
	r.frame(7)
	if got, want := r.settle(), []string{"start7", "stop7", "resume7", "stop7"}; !same(got, want) {
		t.Fatalf("опоздавшее снятие сняло чужое удержание или кадр прошёл: %v", got)
	}
	next()
}

// Голос Алисы не трогаем: у него свой спектр, и удержание его не касается.
func TestHoldLeavesTheVoiceSpectrumAlone(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	release := r.s.Hold()
	defer release()
	r.s.Add(spectrum.Event{Kind: KindStart, Source: SourceVoice, Stream: 9})
	r.s.Add(spectrum.Event{Kind: KindData, Source: SourceVoice, Stream: 9, DB: make([]float32, 513), At: time.Now()})
	if got := r.settle(); len(got) != 4 { // старт и стоп музыки, старт и кадр голоса
		t.Fatalf("ушло %v: голос должен идти как идёт", got)
	}
}

// Пауза: музыка остановилась раньше, чем дошло до удержания, а «остановка» ещё ждёт своего часа (delay после последнего кадра). Платформа рисует кадры,
// пока её не получит, и затёрла бы начало анимации, поэтому удержание торопит остановку и возвращается, когда она уже ушла.
func TestHoldHurriesAPendingStop(t *testing.T) {
	r := newHoldRigDelay(t, 800*time.Millisecond)
	r.ev(KindStart, 7)
	r.ev(KindStop, 7) // уйдёт только через 800 мс
	t0 := time.Now()
	release := r.s.Hold()
	defer release()
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("удержание ждало %v: остановку нужно было поторопить", d)
	}
	got := r.f.got()
	if len(got) != 2 || !bytes.Equal(got[1].msg, Control(uint64(KindStop), 7, SourceMusic)) {
		t.Fatalf("к возврату из удержания остановка должна была уйти, ушло %d сообщений", len(got))
	}
}

// Следующий трек начался, пока держали, и движок объявил его «продолжением» того же потока: платформе после удержания уходит «продолжение», а не «старт».
func TestHoldKeepsTheKindOfTheDeferredAnnouncement(t *testing.T) {
	r := newHoldRig(t)
	r.ev(KindStart, 7)
	release := r.s.Hold()
	r.ev(KindStop, 7)
	r.ev(KindResume, 7)
	release()
	if got, want := r.settle(), []string{"start7", "stop7", "resume7"}; !same(got, want) {
		t.Fatalf("ушло %v, ждали %v", got, want)
	}
}
