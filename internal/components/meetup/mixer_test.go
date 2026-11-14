package meetup

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/core/audio"
)

// capture — приёмник кадров смесителя.
type capture struct {
	mu     sync.Mutex
	frames [][]int16
	silent []bool
}

func (c *capture) out(pcm []int16, silent bool) {
	c.mu.Lock()
	c.frames = append(c.frames, append([]int16(nil), pcm...))
	c.silent = append(c.silent, silent)
	c.mu.Unlock()
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

func (c *capture) frame(i int) ([]int16, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames[i], c.silent[i]
}

func samples(f audio.Format, n int, v ...int16) []byte {
	out := make([]byte, 0, n*f.FrameBytes())
	for i := 0; i < n; i++ {
		for ch := 0; ch < f.Channels; ch++ {
			out = binary.LittleEndian.AppendUint16(out, uint16(v[ch%len(v)]))
		}
	}
	return out
}

func newTestMixer(t *testing.T) (*mixer, *capture) {
	c := &capture{}
	m := startMixer(c.out)
	t.Cleanup(m.Stop)
	return m, c
}

// Стерео сводится в моно полусуммой, громкость применяется к сумме, сумма потоков ограничивается без переполнения.
func TestMixerDownmixGainClamp(t *testing.T) {
	m, c := newTestMixer(t)
	in, err := m.Open(audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	in.Write(samples(audio.Stereo48, proto.FrameSamples, 1000, 3000))
	in.Drain()
	f, silent := c.frame(0)
	if len(f) != proto.FrameSamples || f[0] != 2000 || f[proto.FrameSamples-1] != 2000 || silent {
		t.Fatalf("кадр: %d отсчётов, первый %d, тишина %v", len(f), f[0], silent)
	}

	m.setGain(0.5)
	a, _ := m.Open(audio.Mono48)
	b, _ := m.Open(audio.Mono48)
	n := c.count()
	done := make(chan struct{})
	go func() { b.Write(samples(audio.Mono48, proto.FrameSamples, 30000)); close(done) }()
	a.Write(samples(audio.Mono48, proto.FrameSamples, 30000))
	<-done
	a.Drain()
	b.Drain()
	var peak int16
	for i := n; i < c.count(); i++ {
		f, _ := c.frame(i)
		for _, v := range f {
			peak = max(peak, v)
		}
	}
	// два потока по 30000 в одном кадре дают 60000 × 0,5 = 30000; в разных кадрах — 15000
	if peak != 30000 && peak != 15000 {
		t.Fatalf("пик %d", peak)
	}

	m.setGain(4)
	in, _ = m.Open(audio.Mono48)
	n = c.count()
	in.Write(samples(audio.Mono48, proto.FrameSamples, -20000))
	in.Drain()
	if f, _ := c.frame(n); f[0] != -32768 {
		t.Fatalf("переполнение: %d вместо −32768", f[0])
	}
	if _, err := m.Open(audio.Format{Rate: 44100, Channels: 2}); err == nil {
		t.Fatal("поток 44,1 кГц принят")
	}
}

// Без потоков смеситель спит: ни кадров, ни работы кодера. Поток без звука даёт кадры тишины.
func TestMixerIdleAndSilence(t *testing.T) {
	m, c := newTestMixer(t)
	time.Sleep(80 * time.Millisecond)
	if c.count() != 0 {
		t.Fatalf("без потоков выдано кадров: %d", c.count())
	}
	in, _ := m.Open(audio.Mono48)
	for deadline := time.Now().Add(2 * time.Second); c.count() < 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if c.count() < 2 {
		t.Fatal("поток открыт, а кадров нет")
	}
	if _, silent := c.frame(0); !silent {
		t.Fatal("кадр без звука не помечен тишиной")
	}
	in.Abort()
	time.Sleep(60 * time.Millisecond)
	n := c.count()
	time.Sleep(80 * time.Millisecond)
	if c.count() != n {
		t.Fatal("после ухода потока смеситель продолжает выдавать кадры")
	}
}

// Запись кусками, не кратными отсчёту, не сдвигает байты; Kick снимает потоки, новые после него открываются.
func TestMixerOddWritesAndKick(t *testing.T) {
	m, c := newTestMixer(t)
	in, _ := m.Open(audio.Mono48)
	data := samples(audio.Mono48, proto.FrameSamples, 1234)
	in.Write(data[:3])
	in.Write(data[3:5])
	in.Write(data[5:])
	in.Drain()
	// смеситель мог забрать начало раньше, чем дописан конец: считаем по всем кадрам
	var got int
	for i := 0; i < c.count(); i++ {
		f, _ := c.frame(i)
		for _, v := range f {
			switch v {
			case 1234:
				got++
			case 0:
			default:
				t.Fatalf("отсчёты сдвинулись: в кадре %d значение %d", i, v)
			}
		}
	}
	if got != proto.FrameSamples {
		t.Fatalf("дошло отсчётов: %d из %d", got, proto.FrameSamples)
	}

	in, _ = m.Open(audio.Stereo48)
	blocked := make(chan error, 1)
	go func() { // пишем больше, чем помещается в буфер: Write ждёт смеситель
		var err error
		for err == nil {
			_, err = in.Write(samples(audio.Stereo48, 4800, 1))
		}
		blocked <- err
	}()
	time.Sleep(50 * time.Millisecond)
	m.Kick()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("после Kick запись прошла")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Kick не разбудил ждущую запись")
	}
	in.Drain() // не зависает на снятом потоке
	if m.Streams() != 0 {
		t.Fatalf("потоков после Kick: %d", m.Streams())
	}
	if _, err := m.Open(audio.Mono48); err != nil {
		t.Fatalf("после Kick новый поток не открылся: %v", err)
	}
}
