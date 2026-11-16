package meetup

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/tphakala/go-opus/opus"

	"openmediad/internal/core/audio"
)

// leftTone — стерео-звук, в котором играет только левый канал: по нему видно, дошла ли панорама до комнаты.
func leftTone(d time.Duration) []byte {
	n := int(d * time.Duration(audio.Rate) / time.Second)
	out := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(12000*math.Sin(2*math.Pi*440*float64(i)/audio.Rate))))
		out = binary.LittleEndian.AppendUint16(out, 0)
	}
	return out
}

// Со stereo = true кадры уходят стерео-пакетами Opus: декодер на два канала слышит панораму, а декодер на один канал
// (клиент MeetUp постарше) принимает те же пакеты и сводит их сам — те же 960 отсчётов.
func TestStereoReachesTheRoom(t *testing.T) {
	s := newStand(t)
	s.join()
	s.c.mu.Lock()
	cfg := s.c.cfg
	s.c.mu.Unlock()
	cfg.Stereo = true
	s.c.Reload(&cfg)

	out, err := s.c.Open(audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write(leftTone(600 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	out.Drain()

	st, err := opus.NewDecoder(48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	mono, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 2*5760)
	var left, right, down float64
	for frames := 0; frames < 25; frames++ {
		f := recv(t, s.srv.Audio, "звуковой кадр")
		n, err := st.Decode(f.Payload, pcm)
		if err != nil || n != 960 {
			t.Fatalf("стерео-декодер: %d отсчётов (%v)", n, err)
		}
		if frames >= 5 { // первые кадры — разгон кодера
			for i := 0; i < n; i++ {
				left += math.Abs(float64(pcm[2*i]))
				right += math.Abs(float64(pcm[2*i+1]))
			}
		}
		n, err = mono.Decode(f.Payload, pcm)
		if err != nil || n != 960 {
			t.Fatalf("моно-декодер на стерео-пакете: %d отсчётов (%v): старые клиенты такой кадр выбросят", n, err)
		}
		if frames >= 5 {
			for _, v := range pcm[:n] {
				down += math.Abs(float64(v))
			}
		}
	}
	if left < 1e5 || right > left/4 {
		t.Fatalf("панорама потерялась: левый %.0f, правый %.0f", left, right)
	}
	if down < left/8 {
		t.Fatalf("моно-декодер свёл стерео в тишину: %.0f при левом %.0f", down, left)
	}
}

// Сервер сказал, что слушать некому, — колонка не кодирует и не шлёт; слушатель появился — звук идёт снова.
func TestNoListenersNoSound(t *testing.T) {
	s := newStand(t)
	s.join()
	out, err := s.c.Open(audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Abort()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // музыка играет всё время
		chunk := tone(audio.Stereo48, 100*time.Millisecond)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := out.Write(chunk); err != nil {
				return
			}
		}
	}()
	recv(t, s.srv.Audio, "звук до отказа")

	s.srv.Broadcast(map[string]any{"type": "demand", "audio": false})
	quiet := func() bool { // 300 мс без единого кадра (очередь к сети — до секунды, даём ей вытечь)
		for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
			select {
			case <-s.srv.Audio:
			case <-time.After(300 * time.Millisecond):
				return true
			}
		}
		return false
	}
	if !quiet() {
		t.Fatal("слушать некому, а кадры идут")
	}

	s.srv.Broadcast(map[string]any{"type": "demand", "audio": true})
	recv(t, s.srv.Audio, "звук после возвращения слушателя")
}
