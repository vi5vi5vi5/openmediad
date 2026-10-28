package mp4_test

import (
	"bytes"
	"os"
	"testing"

	"openmediad/internal/codec/mp4"
	"openmediad/internal/codec/mp4/mp4test"
)

func TestReadMoovSynthetic(t *testing.T) {
	file, payload := mp4test.Synth(7, func(i int) uint32 { return uint32(100 + 10*i) })
	x, err := mp4.ReadMoov(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if x.N() != 7 || x.Timescale != 48000 {
		t.Fatalf("N=%d timescale=%d", x.N(), x.Timescale)
	}
	if want := uint64(7 * 1024 * 1000 / 48000); x.DurationMs != want {
		t.Fatalf("длительность %d мс, ждали %d", x.DurationMs, want)
	}
	if !bytes.Equal(x.ASC, []byte{0x11, 0x90}) {
		t.Fatalf("ASC = %x", x.ASC)
	}
	for i := 0; i < 7; i++ {
		got := file[x.Off[i] : x.Off[i]+uint64(x.Size[i])]
		if !bytes.Equal(got, payload[i]) {
			t.Fatalf("кадр %d: смещение %d указывает не туда", i, x.Off[i])
		}
	}
	if x.MoovEnd >= x.Off[0] {
		t.Fatalf("moov заканчивается на %d, первый кадр на %d", x.MoovEnd, x.Off[0])
	}
	// 1024 кадра/48000 = 21,333 мс: кадр 3 начинается на 64 мс, 4-й на 85,3
	if i := x.SampleAtMs(70); i != 3 {
		t.Fatalf("на 70 мс звучит кадр 3, а не %d", i)
	}
	if i := x.SampleAtMs(10_000_000); i != 6 {
		t.Fatalf("за концом — последний кадр, а не %d", i)
	}
	if x.StartMs(3) != 64 {
		t.Fatalf("кадр 3 начинается на %d мс, ждали 64", x.StartMs(3))
	}
}

// TestRealTrack — на настоящем потоке Яндекса (расшифрованный MP4), если путь задан: OMD_TRACK_MP4.
func TestRealTrack(t *testing.T) {
	path := os.Getenv("OMD_TRACK_MP4")
	if path == "" {
		t.Skip("OMD_TRACK_MP4 не задан")
	}
	f, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	x, err := mp4.ReadMoov(bytes.NewReader(f))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("кадров %d, длительность %d мс, timescale %d, ASC %x, moov до %d, первый кадр на %d", x.N(), x.DurationMs, x.Timescale, x.ASC, x.MoovEnd, x.Off[0])
	var end uint64
	for i := 0; i < x.N(); i++ {
		if i > 0 && x.Off[i] < x.Off[i-1] {
			t.Fatalf("смещения не растут на кадре %d", i)
		}
		if e := x.Off[i] + uint64(x.Size[i]); e > end {
			end = e
		}
	}
	if end > uint64(len(f)) {
		t.Fatalf("последний кадр заканчивается на %d за концом файла %d", end, len(f))
	}
	if got := x.StartMs(x.N()); got < x.DurationMs-100 || got > x.DurationMs+100 {
		t.Fatalf("сумма длительностей кадров %d мс не сходится с mvhd %d мс", got, x.DurationMs)
	}
}
