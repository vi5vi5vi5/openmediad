package resample

import (
	"math"
	"testing"
)

// sine — стерео S16LE: левый канал синус freq Гц, правый — silenceRight? тишина : синус вдвое тише.
func sine(rate, freq int, frames int, amp float64, rightToo bool) []byte {
	out := make([]byte, 0, frames*4)
	for i := 0; i < frames; i++ {
		v := int16(amp * math.Sin(2*math.Pi*float64(freq)*float64(i)/float64(rate)))
		out = append(out, byte(v), byte(v>>8))
		w := int16(0)
		if rightToo {
			w = v / 2
		}
		out = append(out, byte(w), byte(w>>8))
	}
	return out
}

func samples(b []byte, ch, which int) []float64 {
	var out []float64
	for i := 0; i+2*ch <= len(b); i += 2 * ch {
		j := i + 2*which
		out = append(out, float64(int16(uint16(b[j])|uint16(b[j+1])<<8)))
	}
	return out
}

// freqOf оценивает частоту по переходам через ноль.
func freqOf(x []float64, rate int) float64 {
	cross := 0
	for i := 1; i < len(x); i++ {
		if (x[i-1] < 0) != (x[i] < 0) {
			cross++
		}
	}
	return float64(cross) / 2 * float64(rate) / float64(len(x))
}

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

func TestUpsample44100To48000KeepsToneAndLevel(t *testing.T) {
	r, err := New(44100, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	in := sine(44100, 1000, 44100, 10000, true)
	out := r.Process(in)
	frames := len(out) / 4
	if frames < 47900 || frames > 48000 { // секунда входа — почти секунда выхода (хвост ждёт следующих отсчётов)
		t.Fatalf("выходных кадров %d, ждали около 48000", frames)
	}
	// отбросить разгон фильтра
	l := samples(out, 2, 0)[200:]
	rr := samples(out, 2, 1)[200:]
	if f := freqOf(l, 48000); math.Abs(f-1000) > 5 {
		t.Fatalf("частота %.1f Гц, ждали 1000", f)
	}
	want := 10000 / math.Sqrt2
	if got := rms(l); math.Abs(got-want) > want*0.01 {
		t.Fatalf("уровень %.0f, ждали %.0f (±1%%)", got, want)
	}
	if got := rms(rr); math.Abs(got-want/2) > want*0.01 {
		t.Fatalf("правый канал %.0f, ждали %.0f", got, want/2)
	}
}

func TestChunkedProcessingEqualsOneShot(t *testing.T) {
	in := sine(44100, 440, 20000, 8000, true)
	a, _ := New(44100, 48000, 2)
	whole := append([]byte(nil), a.Process(in)...)
	b, _ := New(44100, 48000, 2)
	var parts []byte
	for i := 0; i < len(in); {
		n := 4 * (1 + (i/4)%97) // куски разной длины, включая в один кадр
		if i+n > len(in) {
			n = len(in) - i
		}
		parts = append(parts, b.Process(in[i:i+n])...)
		i += n
	}
	if len(whole) != len(parts) {
		t.Fatalf("длина: целиком %d, кусками %d", len(whole), len(parts))
	}
	for i := range whole {
		if whole[i] != parts[i] {
			t.Fatalf("расхождение на байте %d: стыки кусков слышны", i)
		}
	}
}

func TestSilenceAndDCStayStable(t *testing.T) {
	r, _ := New(44100, 48000, 2)
	out := r.Process(make([]byte, 4*5000))
	for _, v := range samples(out, 2, 0) {
		if v != 0 {
			t.Fatal("тишина превратилась в шум")
		}
	}
	dc := make([]byte, 0, 4*5000)
	for i := 0; i < 5000; i++ {
		dc = append(dc, 0x10, 0x27, 0x10, 0x27) // 10000
	}
	out = r.Process(dc)
	for _, v := range samples(out, 2, 0)[100:] {
		if math.Abs(v-10000) > 5 {
			t.Fatalf("постоянная составляющая уплыла: %.0f", v)
		}
	}
}

func TestSameRateIsIdentityDelayedByFilter(t *testing.T) {
	r, _ := New(48000, 48000, 1)
	in := sine(48000, 500, 4000, 9000, false)
	mono := make([]byte, 0, 2*4000)
	for i := 0; i < len(in); i += 4 {
		mono = append(mono, in[i], in[i+1])
	}
	out := r.Process(mono)
	got := samples(out, 1, 0)
	want := samples(mono, 1, 0)
	if len(got) < 3900 {
		t.Fatalf("выход %d кадров", len(got))
	}
	// отсчёты совпадают после задержки фильтра (left кадров тишины в начале не добавляются: выход идёт с опорного кадра)
	for i := 100; i < len(got)-100; i++ {
		if math.Abs(got[i]-want[i]) > 3 {
			t.Fatalf("кадр %d: %.0f против %.0f", i, got[i], want[i])
		}
	}
}

func TestDownsampleCutsFrequenciesAboveNewNyquist(t *testing.T) {
	r, err := New(96000, 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(freq int) []byte {
		var b []byte
		for i := 0; i < 96000; i++ {
			v := int16(10000 * math.Sin(2*math.Pi*float64(freq)*float64(i)/96000))
			b = append(b, byte(v), byte(v>>8))
		}
		return b
	}
	pass := samples(r.Process(mk(5000)), 1, 0)[500:]
	r.Reset()
	stop := samples(r.Process(mk(30000)), 1, 0)[500:] // выше Найквиста выхода (24 кГц): должен погаснуть
	if rms(pass) < 6500 {
		t.Fatalf("полоса пропускания ослаблена: %.0f", rms(pass))
	}
	if rms(stop) > 100 {
		t.Fatalf("то, что не помещается в 48 кГц, не срезано: %.0f", rms(stop))
	}
}

func TestResetForgetsHistory(t *testing.T) {
	r, _ := New(44100, 48000, 2)
	r.Process(sine(44100, 1000, 3000, 20000, true))
	r.Reset()
	out := r.Process(make([]byte, 4*2000))
	for _, v := range samples(out, 2, 0) {
		if v != 0 {
			t.Fatal("после Reset в выходе остатки прежнего звука")
		}
	}
}

func TestClipsInsteadOfWrapping(t *testing.T) {
	r, _ := New(44100, 48000, 1)
	var b []byte
	for i := 0; i < 4000; i++ { // громкий прямоугольник: фильтр даёт выбросы выше полной шкалы
		v := int16(32767)
		if (i/20)%2 == 1 {
			v = -32768
		}
		b = append(b, byte(v), byte(v>>8))
	}
	for _, v := range samples(r.Process(b), 1, 0) {
		if v > 32767 || v < -32768 {
			t.Fatalf("отсчёт %.0f вне диапазона", v)
		}
	}
}

func TestBadParameters(t *testing.T) {
	for _, c := range [][3]int{{0, 48000, 2}, {44100, 0, 2}, {44100, 48000, 0}, {44100, 48000, 9}} {
		if _, err := New(c[0], c[1], c[2]); err == nil {
			t.Errorf("New%v принят", c)
		}
	}
}

func BenchmarkSecondOfStereo(b *testing.B) {
	r, _ := New(44100, 48000, 2)
	in := sine(44100, 1000, 44100, 10000, true)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		r.Process(in)
	}
}
