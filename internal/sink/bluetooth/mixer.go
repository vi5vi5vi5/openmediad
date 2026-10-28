package bluetooth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"openmediad/internal/audio"
)

// Смеситель наушников: один вывод (aplay в PCM BlueALSA) на всё время, пока наушники подключены. Потоки движка
// (музыка, голос, звонки) пишут в него каждый в свой буфер, смеситель складывает их порциями по 20 мс, а в паузах пишет
// тишину. Зачем:
//   - платформа считает любой ставший активным транспорт A2DP звуком телефона: забирает аудиофокус и отменяет музыку
//     (docs/BUS-PROTOCOL.md, §2.7б). Пока канал открыт постоянно, транспорт включается один раз за подключение, и его
//     достаточно один раз спрятать от платформы (bluez.ForgetTransport);
//   - PCM BlueALSA принимает одного клиента: без смесителя ответ Алисы поверх музыки в наушники не попал бы;
//   - не нужно ждать запуска потока A2DP на каждом ответе Алисы.

const (
	mixChunk    = 20 * time.Millisecond
	mixBytes    = 48000 * 4 / 50 // 20 мс стерео S16 48 кГц
	inputBytes  = 48000 * 4 / 10 // буфер каждого потока: 100 мс
	mixPipe     = 4096           // труба к aplay: ≈21 мс (полная системная — это ещё 0,34 с задержки)
	mixBufferUS = 150 * 1000     // буфер aplay в PCM BlueALSA
)

// errMixerClosed — смеситель остановлен (наушники отключились или выход выключен).
var errMixerClosed = errors.New("смеситель наушников остановлен")

type mixer struct {
	out audio.Output

	mu   sync.Mutex
	cond *sync.Cond
	ins  []*mixInput
	err  error
	done chan struct{}
}

// startMixer открывает вывод в наушники (48 кГц стерео) и начинает писать в него: звук потоков или тишину.
func startMixer(open func() (audio.Output, error)) (*mixer, error) {
	out, err := open()
	if err != nil {
		return nil, err
	}
	m := &mixer{out: out, done: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	go m.loop()
	return m, nil
}

func (m *mixer) loop() {
	defer close(m.done)
	acc := make([]int32, mixBytes/2)
	buf := make([]byte, mixBytes)
	for {
		m.mu.Lock()
		if m.err != nil {
			m.mu.Unlock()
			return
		}
		for i := range acc {
			acc[i] = 0
		}
		live := m.ins[:0]
		for _, in := range m.ins {
			in.mixInto(acc)
			if in.finished() {
				in.state = inDone
				continue
			}
			live = append(live, in)
		}
		for i := len(live); i < len(m.ins); i++ {
			m.ins[i] = nil
		}
		m.ins = live
		m.cond.Broadcast() // место в буферах освободилось, допитые потоки — закончены
		m.mu.Unlock()
		for i, v := range acc {
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			binary.LittleEndian.PutUint16(buf[2*i:], uint16(int16(v)))
		}
		if _, err := m.out.Write(buf); err != nil {
			m.fail(fmt.Errorf("вывод в наушники: %w", err))
			return
		}
	}
}

// fail останавливает смеситель: все потоки получают ошибку записи (движок переведёт их на другой выход).
func (m *mixer) fail(err error) {
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	m.cond.Broadcast()
	m.mu.Unlock()
}

// Stop останавливает смеситель и обрывает вывод.
func (m *mixer) Stop() {
	m.fail(errMixerClosed)
	m.out.Abort()
	<-m.done
}

// Err — почему смеситель остановился (nil — работает).
func (m *mixer) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Streams — сколько потоков сейчас звучит.
func (m *mixer) Streams() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ins)
}

// Open — новый поток: вывод движка (audio.Output) в смеситель. Только 48 кГц, моно или стерео.
func (m *mixer) Open(f audio.Format) (audio.Output, error) {
	if f.Rate != 48000 || f.Channels < 1 || f.Channels > 2 {
		return nil, fmt.Errorf("наушники: формат %d Гц × %d кан. не поддерживается (нужно 48 кГц, моно или стерео)", f.Rate, f.Channels)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	in := &mixInput{m: m, mono: f.Channels == 1}
	m.ins = append(m.ins, in)
	return in, nil
}

const (
	inPlaying = iota
	inDraining
	inDone
	inAborted
)

// mixInput — поток в смесителе. Write блокируется, пока буфер (100 мс) полон: это и есть темп воспроизведения.
type mixInput struct {
	m     *mixer
	mono  bool
	buf   []byte // стерео S16
	state int
}

func (in *mixInput) Write(p []byte) (int, error) {
	m := in.m
	data := p
	if in.mono { // голос Алисы — моно: в наушники идёт стерео
		data = make([]byte, len(p)/2*4)
		for i := 0; i+1 < len(p); i += 2 {
			copy(data[i*2:], p[i:i+2])
			copy(data[i*2+2:], p[i:i+2])
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(data) > 0 {
		switch {
		case m.err != nil:
			return 0, m.err
		case in.state == inAborted:
			return 0, errors.New("поток наушников оборван")
		}
		room := inputBytes - len(in.buf)
		if room <= 0 {
			m.cond.Wait()
			continue
		}
		n := min(room, len(data))
		in.buf = append(in.buf, data[:n]...)
		data = data[n:]
	}
	return len(p), nil
}

// mixInto добавляет к acc следующую порцию потока (под m.mu).
func (in *mixInput) mixInto(acc []int32) {
	n := min(len(in.buf), len(acc)*2)
	for i := 0; i+1 < n; i += 2 {
		acc[i/2] += int32(int16(binary.LittleEndian.Uint16(in.buf[i:])))
	}
	in.buf = in.buf[n:]
	if len(in.buf) == 0 {
		in.buf = in.buf[:0:0] // не держать большой массив
	}
}

func (in *mixInput) finished() bool {
	return in.state == inAborted || in.state == inDraining && len(in.buf) == 0
}

// Drain ждёт, пока смеситель заберёт остаток (и он прозвучит в порядке очереди), и убирает поток.
func (in *mixInput) Drain() {
	m := in.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.state == inPlaying {
		in.state = inDraining
	}
	for in.state != inDone && in.state != inAborted && m.err == nil {
		m.cond.Wait()
	}
}

// Abort убирает поток сразу: недоигранный остаток выбрасывается.
func (in *mixInput) Abort() {
	m := in.m
	m.mu.Lock()
	in.state = inAborted
	in.buf = nil
	m.cond.Broadcast()
	m.mu.Unlock()
}
