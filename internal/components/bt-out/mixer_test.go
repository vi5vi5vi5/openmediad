package btout

import (
	"encoding/binary"
	"testing"
	"time"

	"openmediad/internal/core/audio"
)

func s16(vals ...int16) []byte {
	b := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func samples(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

func newTestMixer(t *testing.T, o *memOut) *mixer {
	m, err := startMixer(func() (audio.Output, error) { return o, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	return m
}

// together пишет в потоки, пока смеситель стоит на записи в вывод: следующая порция гарантированно содержит их все.
// Возвращает, сколько байт было в выводе до этой порции.
func together(t *testing.T, o *memOut, writes func()) int {
	t.Helper()
	o.hold.Lock()
	waitFor(t, "смеситель ждёт вывод", func() bool { return o.blocked.Load() == 1 })
	writes()
	before := len(o.data())
	o.hold.Unlock()
	return before
}

// Без потоков смеситель пишет тишину (канал не закрывается); два потока складываются, моно становится стерео.
func TestMixerSumsStreamsAndKeepsSilence(t *testing.T) {
	o := &memOut{}
	m := newTestMixer(t, o)
	waitFor(t, "тишина в паузе", func() bool { return len(o.data()) >= 2*mixBytes })
	for _, v := range samples(o.data()) {
		if v != 0 {
			t.Fatal("в паузе — только тишина")
		}
	}
	music, _ := m.Open(audio.Stereo48)
	voice, _ := m.Open(audio.Format{Rate: 48000, Channels: 1})
	// 20 мс: музыка (L=1000, R=-1000), голос моно 300 → L=1300, R=-700
	st := make([]int16, 0, 1920)
	for i := 0; i < 960; i++ {
		st = append(st, 1000, -1000)
	}
	mono := make([]int16, 960)
	for i := range mono {
		mono[i] = 300
	}
	before := together(t, o, func() {
		music.Write(s16(st...))
		voice.Write(s16(mono...))
	})
	music.Drain()
	voice.Drain()
	got := samples(o.data()[before:])
	found := false
	for i := 0; i+1 < len(got); i += 2 {
		if got[i] == 1300 && got[i+1] == -700 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("в канале нет суммы потоков (1300, -700)")
	}
	if m.Streams() != 0 {
		t.Fatal("доигранные потоки убраны")
	}
}

func TestMixerClipsInsteadOfWrapping(t *testing.T) {
	o := &memOut{}
	m := newTestMixer(t, o)
	a, _ := m.Open(audio.Stereo48)
	b, _ := m.Open(audio.Stereo48)
	loud := make([]int16, 1920)
	for i := range loud {
		loud[i] = 30000
	}
	before := together(t, o, func() {
		a.Write(s16(loud...))
		b.Write(s16(loud...))
	})
	a.Drain()
	b.Drain()
	sawClip := false
	for _, v := range samples(o.data()[before:]) {
		if v < 0 {
			t.Fatal("переполнение должно обрезаться, а не заворачиваться в минус")
		}
		if v == 32767 {
			sawClip = true
		}
	}
	if !sawClip {
		t.Fatal("сумма 60000 должна упереться в 32767")
	}
}

// Наушники отключились (aplay вышел): все потоки получают ошибку записи — движок переведёт их на другой выход.
func TestMixerFailureErrorsStreams(t *testing.T) {
	o := &memOut{failAt: 4 * mixBytes}
	m := newTestMixer(t, o)
	in, _ := m.Open(audio.Stereo48)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := in.Write(make([]byte, mixBytes)); err != nil {
			if m.Err() == nil {
				t.Fatal("ошибка потока — значит, смеситель остановлен")
			}
			return
		}
	}
	t.Fatal("запись не вернула ошибку")
}

// Оборванный поток не держит смеситель: Write после Abort — ошибка, а сам поток убирается.
func TestMixerAbortDropsStream(t *testing.T) {
	o := &memOut{}
	m := newTestMixer(t, o)
	in, _ := m.Open(audio.Stereo48)
	in.Write(make([]byte, inputBytes))
	in.Abort()
	if _, err := in.Write(make([]byte, 4)); err == nil {
		t.Fatal("после Abort запись — ошибка")
	}
	waitFor(t, "поток убран", func() bool { return m.Streams() == 0 })
}
