package btout

import (
	"testing"
	"time"

	"openmediad/internal/core/audio"
)

// Поток смесителя сообщает движку хвост вывода (audio.Tailer): сколько звук идёт до ушей после того, как смеситель его забрал.
func TestMixerInputReportsTail(t *testing.T) {
	m := newTestMixer(t, &memOut{})
	in, err := m.Open(audio.Mono48)
	if err != nil {
		t.Fatal(err)
	}
	tl, ok := in.(audio.Tailer)
	if !ok {
		t.Fatal("поток смесителя должен уметь Tail")
	}
	if tl.Tail() != 0 {
		t.Fatalf("пока хвост не задан, он нулевой: %v", tl.Tail())
	}
	m.setTail(400 * time.Millisecond)
	if tl.Tail() != 400*time.Millisecond {
		t.Fatalf("хвост %v, ждали 400 мс", tl.Tail())
	}
}

// Оценка хвоста: труба и буфер aplay плюс задержка наушников от BlueALSA; неправдоподобная или неизвестная заменяется типичной,
// настройка latency перекрывает оценку.
func TestOutputTailEstimate(t *testing.T) {
	fixed := time.Duration(mixPipe)*time.Second/(48000*4) + mixBufferUS*time.Microsecond
	s := &Sink{}
	for _, c := range []struct {
		delay, want time.Duration
	}{
		{240 * time.Millisecond, fixed + 240*time.Millisecond},
		{0, fixed + typicalHeadphoneDelay},
		{15 * time.Millisecond, fixed + typicalHeadphoneDelay},
		{5 * time.Second, fixed + typicalHeadphoneDelay},
	} {
		if got := s.outputTail(&target{delay: c.delay}); got != c.want {
			t.Errorf("задержка %v: хвост %v, ждали %v", c.delay, got, c.want)
		}
	}
	s.Latency = 500 * time.Millisecond
	if got := s.outputTail(&target{delay: 240 * time.Millisecond}); got != 500*time.Millisecond {
		t.Errorf("настройка latency должна перекрывать оценку: %v", got)
	}
}
