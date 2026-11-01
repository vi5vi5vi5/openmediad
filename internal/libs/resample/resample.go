// Package resample — передискретизация PCM S16LE в другую частоту полифазным фильтром (sinc с окном Кайзера).
//
// Нужна, когда источник играет не в 48 кГц, а выход (наушники через смеситель bt-out, штатный путь платформы) принимает только 48 кГц:
// Spotify декодирует в 44,1 кГц. Отношение частот рациональное (44100 → 48000 = 160/147), поэтому используется точная полифазная схема:
// для каждой из L фаз выходного отсчёта заранее посчитаны коэффициенты, на выходной отсчёт приходится одна свёртка из taps слагаемых.
// Состояние держится между вызовами Process, границы кусков звука не слышны.
package resample

import (
	"errors"
	"fmt"
	"math"
)

const (
	// halfTaps — сколько входных отсчётов фильтр берёт с каждой стороны (на единицу отношения частот).
	halfTaps = 16
	// cutoff — граница полосы пропускания в долях частоты Найквиста более низкой из двух частот.
	cutoff = 0.95
	// kaiserBeta — параметр окна: подавление за полосой около 80 дБ.
	kaiserBeta = 8.0
)

// Resampler — потоковый передискретизатор. Для одного потока звука; не потокобезопасен.
type Resampler struct {
	ch      int
	l, m    int       // выходная частота / g и входная / g
	taps    int       // длина фильтра в отсчётах
	left    int       // сколько отсчётов слева от опорного берёт фильтр
	coef    []float32 // l фаз по taps коэффициентов
	buf     []float32 // входные кадры (чередование каналов), в начале left кадров истории
	i       int       // опорный входной кадр следующего выходного отсчёта (индекс в buf)
	num     int       // фаза следующего выходного отсчёта, 0 ≤ num < l
	scratch []byte
}

// New создаёт передискретизатор из in Гц в out Гц для channels каналов (1…8).
func New(in, out, channels int) (*Resampler, error) {
	if in <= 0 || out <= 0 || channels < 1 || channels > 8 {
		return nil, fmt.Errorf("resample: частоты %d → %d, каналов %d", in, out, channels)
	}
	g := gcd(in, out)
	l, m := out/g, in/g
	if l > 4096 {
		return nil, errors.New("resample: отношение частот слишком сложное")
	}
	// при понижении частоты фильтр растягивается, чтобы срезать то, что не поместится в новую полосу
	scale := math.Max(1, float64(in)/float64(out))
	half := int(math.Ceil(halfTaps * scale))
	r := &Resampler{ch: channels, l: l, m: m, taps: 2 * half, left: half - 1}
	fc := cutoff * 0.5 / scale // в долях входной частоты дискретизации
	r.coef = make([]float32, l*r.taps)
	for p := 0; p < l; p++ {
		frac := float64(p) / float64(l)
		var sum float64
		row := make([]float64, r.taps)
		for k := 0; k < r.taps; k++ {
			t := float64(k-r.left) - frac // расстояние от выходного отсчёта до входного
			row[k] = sinc(2*fc*t) * 2 * fc * kaiser(t/float64(half), kaiserBeta)
			sum += row[k]
		}
		for k := range row { // единичное усиление на постоянной составляющей в каждой фазе
			r.coef[p*r.taps+k] = float32(row[k] / sum)
		}
	}
	r.Reset()
	return r, nil
}

// Reset забывает накопленное (перемотка, новый трек): дальше фильтр видит тишину.
func (r *Resampler) Reset() {
	r.buf = make([]float32, r.left*r.ch)
	r.i, r.num = r.left, 0
}

// Ratio — выходных кадров на входной кадр.
func (r *Resampler) Ratio() float64 { return float64(r.l) / float64(r.m) }

// Process принимает кадры S16LE (целое число кадров; хвост неполного кадра отбрасывается) и возвращает готовые выходные кадры.
// Часть выхода откладывается до прихода следующих отсчётов: фильтру нужны отсчёты справа.
func (r *Resampler) Process(in []byte) []byte {
	fb := 2 * r.ch
	n := len(in) / fb
	for j := 0; j < n*r.ch; j++ {
		r.buf = append(r.buf, float32(int16(uint16(in[2*j])|uint16(in[2*j+1])<<8)))
	}
	frames := len(r.buf) / r.ch
	right := r.taps - r.left - 1
	out := r.scratch[:0]
	for r.i+right < frames {
		c := r.coef[r.num*r.taps : (r.num+1)*r.taps]
		base := (r.i - r.left) * r.ch
		for ch := 0; ch < r.ch; ch++ {
			var acc float32
			for k, w := range c {
				acc += w * r.buf[base+k*r.ch+ch]
			}
			v := int32(math.Floor(float64(acc) + 0.5))
			if v > math.MaxInt16 {
				v = math.MaxInt16
			} else if v < math.MinInt16 {
				v = math.MinInt16
			}
			out = append(out, byte(v), byte(v>>8))
		}
		r.num += r.m
		r.i += r.num / r.l
		r.num %= r.l
	}
	// выбросить то, что фильтру уже не понадобится, оставив историю слева от опорного кадра
	if drop := r.i - r.left; drop > 0 {
		r.buf = append(r.buf[:0], r.buf[drop*r.ch:]...)
		r.i -= drop
	}
	r.scratch = out
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// kaiser — окно Кайзера на отрезке [-1, 1].
func kaiser(x, beta float64) float64 {
	if x <= -1 || x >= 1 {
		return 0
	}
	return bessel0(beta*math.Sqrt(1-x*x)) / bessel0(beta)
}

// bessel0 — модифицированная функция Бесселя первого рода нулевого порядка (ряд).
func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / 2 / float64(k)) * (x / 2 / float64(k))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}
