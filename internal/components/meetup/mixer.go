package meetup

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/core/audio"
)

// Смеситель конференции: потоки движка (музыка, голос Алисы) пишут в него каждый в свой буфер, смеситель складывает их в один
// моно-кадр 20 мс и отдаёт дальше (кодеру). Устроен как смеситель наушников (bt-out/mixer.go), с тремя отличиями:
//   - звук сводится в моно: клиенты MeetUp принимают только Opus 48 кГц моно;
//   - темп задают часы, а не устройство вывода: за смесителем сеть, она темпа не держит;
//   - без потоков смеситель спит и процессор не занимает.

const (
	frameDur     = 20 * time.Millisecond
	mixInputSize = 48000 * 2 / 10 // буфер каждого потока: 100 мс моно S16
	maxLag       = 200 * time.Millisecond
)

// errKicked — потоки сняты со смесителя: конференция недоступна, движок переведёт их на следующий выход маршрута.
var errKicked = errors.New("конференция MeetUp недоступна")

type mixer struct {
	// out получает каждый смешанный кадр (960 отсчётов моно); silent — кадр из одних нулей. Зовётся из горутины смесителя.
	out func(pcm []int16, silent bool)

	gain atomic.Int32 // усиление в 1/1024

	mu     sync.Mutex
	cond   *sync.Cond
	ins    []*mixInput
	closed bool
	done   chan struct{}
}

func startMixer(out func(pcm []int16, silent bool)) *mixer {
	m := &mixer{out: out, done: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	m.gain.Store(1024)
	go m.loop()
	return m
}

// setGain задаёт усиление смешанного звука (1 — без изменений).
func (m *mixer) setGain(g float64) { m.gain.Store(int32(g*1024 + 0.5)) }

func (m *mixer) loop() {
	defer close(m.done)
	acc := make([]int32, proto.FrameSamples)
	pcm := make([]int16, proto.FrameSamples)
	next := time.Now()
	for {
		m.mu.Lock()
		for len(m.ins) == 0 && !m.closed { // потоков нет: спим, пока не появится
			m.cond.Wait()
			next = time.Now()
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		clear(acc)
		live := m.ins[:0]
		for _, in := range m.ins {
			in.mixInto(acc)
			if in.finished() {
				if in.state == inDraining {
					in.state = inDone
				}
				continue
			}
			live = append(live, in)
		}
		clear(m.ins[len(live):])
		m.ins = live
		m.cond.Broadcast() // место в буферах освободилось, допитые потоки закончены
		m.mu.Unlock()

		g := int64(m.gain.Load())
		silent := true
		for i, v := range acc {
			v = int32(int64(v) * g >> 10)
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			if v != 0 {
				silent = false
			}
			pcm[i] = int16(v)
		}
		m.out(pcm, silent)

		next = next.Add(frameDur)
		switch d := time.Until(next); {
		case d < -maxLag: // отстали (колонка была занята): не догоняем пачкой, начинаем отсчёт заново
			next = time.Now()
		case d > 0:
			time.Sleep(d)
		}
	}
}

// Open — новый поток движка в смеситель. Только 48 кГц, моно или стерео.
func (m *mixer) Open(f audio.Format) (audio.Output, error) {
	if f.Rate != audio.Rate || f.Channels < 1 || f.Channels > 2 {
		return nil, fmt.Errorf("конференция: формат %d Гц × %d кан. не поддерживается (нужно 48 кГц, моно или стерео)", f.Rate, f.Channels)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errKicked
	}
	in := &mixInput{m: m, stereo: f.Channels == 2}
	m.ins = append(m.ins, in)
	m.cond.Broadcast()
	return in, nil
}

// Kick снимает все потоки: их Write вернёт ошибку, и движок переведёт их на следующий выход маршрута. Смеситель остаётся.
func (m *mixer) Kick() {
	m.mu.Lock()
	for _, in := range m.ins {
		in.state = inAborted
		in.buf = nil
	}
	clear(m.ins)
	m.ins = m.ins[:0]
	m.cond.Broadcast()
	m.mu.Unlock()
}

// Stop останавливает смеситель насовсем.
func (m *mixer) Stop() {
	m.mu.Lock()
	m.closed = true
	for _, in := range m.ins {
		in.state = inAborted
		in.buf = nil
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	<-m.done
}

// Streams — сколько потоков сейчас в смесителе.
func (m *mixer) Streams() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ins)
}

const (
	inPlaying = iota
	inDraining
	inDone
	inAborted
)

// mixInput — поток в смесителе. Write блокируется, пока буфер (100 мс) полон: это и есть темп воспроизведения.
type mixInput struct {
	m      *mixer
	stereo bool
	buf    []byte // моно S16LE
	state  int
	odd    []byte // неполный отсчёт с прошлой записи (движок пишет целыми отсчётами, но договор Output этого не обещает)
}

func (in *mixInput) Write(p []byte) (int, error) {
	m := in.m
	frame := 2
	if in.stereo {
		frame = 4
	}
	data := p
	if len(in.odd) > 0 {
		data = append(in.odd, p...)
		in.odd = nil
	}
	if rest := len(data) % frame; rest != 0 {
		in.odd = append([]byte(nil), data[len(data)-rest:]...)
		data = data[:len(data)-rest]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(data) > 0 {
		if m.closed || in.state == inAborted || in.state == inDone {
			return 0, errKicked
		}
		room := (mixInputSize - len(in.buf)) / 2 // сколько отсчётов поместится
		if room <= 0 {
			m.cond.Wait()
			continue
		}
		n := min(room, len(data)/frame)
		if in.stereo { // стерео → моно: полусумма каналов
			for i := 0; i < n; i++ {
				l := int32(int16(binary.LittleEndian.Uint16(data[4*i:])))
				r := int32(int16(binary.LittleEndian.Uint16(data[4*i+2:])))
				in.buf = binary.LittleEndian.AppendUint16(in.buf, uint16(int16((l+r)/2)))
			}
		} else {
			in.buf = append(in.buf, data[:2*n]...)
		}
		data = data[n*frame:]
	}
	return len(p), nil
}

// mixInto добавляет к acc следующий кадр потока (под m.mu); чего в буфере не хватило — тишина.
func (in *mixInput) mixInto(acc []int32) {
	n := min(len(in.buf)/2, len(acc))
	for i := 0; i < n; i++ {
		acc[i] += int32(int16(binary.LittleEndian.Uint16(in.buf[2*i:])))
	}
	in.buf = in.buf[2*n:]
	if len(in.buf) == 0 {
		in.buf = nil // не держать большой массив
	}
}

func (in *mixInput) finished() bool {
	return in.state == inAborted || in.state == inDraining && len(in.buf) == 0
}

// Drain ждёт, пока смеситель заберёт остаток, и убирает поток.
func (in *mixInput) Drain() {
	m := in.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.state == inPlaying {
		in.state = inDraining
	}
	for in.state != inDone && in.state != inAborted && !m.closed {
		m.cond.Wait()
	}
}

// Abort убирает поток сразу: недоигранный остаток выбрасывается.
func (in *mixInput) Abort() {
	m := in.m
	m.mu.Lock()
	if in.state != inDone {
		in.state = inAborted
	}
	in.buf = nil
	m.cond.Broadcast()
	m.mu.Unlock()
}
