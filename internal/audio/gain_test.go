package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

func TestApplyGainRampsAndClips(t *testing.T) {
	mk := func(vals ...int16) []byte {
		b := make([]byte, 2*len(vals))
		for i, v := range vals {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
		}
		return b
	}
	b := mk(1000, 1000, 1000, 1000)
	if g := ApplyGain(b, 1, 1); g != 1 || !bytes.Equal(b, mk(1000, 1000, 1000, 1000)) {
		t.Fatal("громкость 1 не должна менять отсчёты")
	}
	b = mk(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000)
	g := ApplyGain(b, 1, 0.25)
	if g != 0.75 {
		t.Fatalf("за порцию громкость сдвигается на шаг 0,25: %v", g)
	}
	first := int16(binary.LittleEndian.Uint16(b[0:]))
	last := int16(binary.LittleEndian.Uint16(b[14:]))
	if first != 1000 || last >= first {
		t.Fatalf("плавное снижение: первый %d, последний %d", first, last)
	}
	b = mk(30000, -30000)
	ApplyGain(b, 2, 2)
	if int16(binary.LittleEndian.Uint16(b[0:])) != 32767 || int16(binary.LittleEndian.Uint16(b[2:])) != -32768 {
		t.Fatal("переполнение должно ограничиваться, а не заворачиваться")
	}
}

func TestFadeInRampsFromSilence(t *testing.T) {
	const fadeInBytes = 150 * 192
	b := make([]byte, fadeInBytes+400)
	for i := 0; i+1 < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], uint16(int16(10000)))
	}
	left := FadeIn(b, fadeInBytes, fadeInBytes)
	if left != 0 {
		t.Fatalf("после полного окна нарастание должно закончиться, осталось %d", left)
	}
	first := int16(binary.LittleEndian.Uint16(b[0:]))
	mid := int16(binary.LittleEndian.Uint16(b[fadeInBytes/2:]))
	end := int16(binary.LittleEndian.Uint16(b[fadeInBytes+100:]))
	if first > 100 || mid < 4000 || mid > 6000 || end != 10000 {
		t.Fatalf("нарастание: начало %d (ждали ≈0), середина %d (≈5000), после окна %d (10000)", first, mid, end)
	}
	// нарастание, разбитое на порции, даёт тот же результат, что целиком
	c := make([]byte, fadeInBytes)
	for i := 0; i+1 < len(c); i += 2 {
		binary.LittleEndian.PutUint16(c[i:], uint16(int16(10000)))
	}
	rem := fadeInBytes
	for off := 0; off < len(c); off += 9600 {
		rem = FadeIn(c[off:min(off+9600, len(c))], rem, fadeInBytes)
	}
	if !bytes.Equal(c, b[:fadeInBytes]) {
		t.Fatal("нарастание по порциям отличается от нарастания целиком")
	}
}

// applyGain в целых числах не должна расходиться с расчётом в плавающей точке больше чем на единицу младшего разряда.
func TestApplyGainMatchesFloatReference(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for _, c := range []struct{ cur, target float64 }{{0.4, 0.4}, {1, 0.25}, {0.25, 1}, {0.7, 0.72}, {1, 2}, {0.43, 0.43}} {
		b := make([]byte, 2*5000)
		ref := make([]float64, 5000)
		for i := range ref {
			v := int16(r.Intn(65536) - 32768)
			binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
			ref[i] = float64(v)
		}
		next := ApplyGain(b, c.cur, c.target)
		step := next
		if d := c.target - c.cur; math.Abs(d) > RampStep {
			step = c.cur + math.Copysign(RampStep, d)
		}
		if next != step {
			t.Fatalf("громкость к концу порции %v, ждали %v", next, step)
		}
		for i := range ref {
			g := c.cur + (next-c.cur)*float64(i)/float64(len(ref))
			want := math.Max(-32768, math.Min(32767, math.Round(ref[i]*g)))
			got := float64(int16(binary.LittleEndian.Uint16(b[2*i:])))
			if math.Abs(got-want) > 1 {
				t.Fatalf("громкость %v→%v, отсчёт %d: %v, ждали %v", c.cur, c.target, i, got, want)
			}
		}
	}
}
