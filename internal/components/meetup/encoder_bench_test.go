package meetup

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/tphakala/go-opus/opus"
)

// musicLike — секунда звука, похожего на музыку (аккорд с обертонами, удары и шум): кодеру на нём не легче, чем на настоящем треке.
func musicLike(channels int) []int16 {
	const rate = 48000
	rng := rand.New(rand.NewSource(1))
	pcm := make([]int16, rate*channels)
	freqs := []float64{110, 220, 277.18, 329.63, 440, 880, 1760, 3520, 7040}
	for i := 0; i < rate; i++ {
		t := float64(i) / rate
		var v float64
		for k, f := range freqs {
			v += math.Sin(2*math.Pi*f*t+float64(k)) / float64(k+2)
		}
		beat := math.Exp(-math.Mod(t, 0.25) * 30) // «бочка» четыре раза в секунду
		v += beat * math.Sin(2*math.Pi*60*t) * 1.5
		v += (rng.Float64() - 0.5) * 0.2 * (0.3 + beat)
		s := int16(v * 6000)
		for c := 0; c < channels; c++ {
			pcm[i*channels+c] = s
		}
	}
	return pcm
}

// Сколько процессора занимает кодер Opus. Запуск на колонке:
//
//	GOOS=linux GOARCH=arm64 go test -c -o meetup.test ./internal/components/meetup
//	scp -P 2222 meetup.test root@колонка:/tmp/ && ssh -p 2222 root@колонка /tmp/meetup.test -test.run '^$' -test.bench Opus
//
// Метрика «ms/s» — миллисекунд работы на секунду звука: 10 ms/s = 1 % одного ядра.
func BenchmarkOpusEncode(b *testing.B) {
	for _, ch := range []int{1, 2} {
		pcm := musicLike(ch)
		for _, cx := range []int{1, 3, 5, 8, 10} {
			b.Run(fmt.Sprintf("ch%d/complexity%d", ch, cx), func(b *testing.B) {
				enc, err := opus.NewEncoder(opus.EncoderConfig{SampleRate: 48000, Channels: ch, Bitrate: 64000 * ch, Complexity: cx})
				if err != nil {
					b.Fatal(err)
				}
				out := make([]byte, 1500)
				frame := 960 * ch
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for off := 0; off+frame <= len(pcm); off += frame {
						if _, err := enc.Encode(pcm[off:off+frame], out); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.ReportMetric(float64(b.Elapsed().Milliseconds())/float64(b.N), "ms/s")
			})
		}
	}
}
