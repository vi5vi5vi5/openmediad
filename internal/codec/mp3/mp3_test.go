package mp3

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Начала настоящих файлов из /system/vendor/quasar/sounds/common/ (заголовок первого кадра и заголовок Xing/Info; дальше
// звук, он для разбора не нужен).
var (
	timerHead = []byte{ // timer.wav: MPEG1 layer III 64 кбит/с 44,1 кГц моно, Info: 354 кадра, 74186 байт
		0xff, 0xfb, 0x50, 0xc4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'I', 'n', 'f', 'o',
		0, 0, 0, 0x0f, 0, 0, 0x01, 0x62, 0, 0x01, 0x21, 0xca}
	spotterHead = []byte{ // spotter_start.mp3: первый кадр 128 кбит/с 48 кГц моно, Xing: 24 кадра, 5736 байт (переменный битрейт)
		0xff, 0xfb, 0x94, 0xc4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'X', 'i', 'n', 'g',
		0, 0, 0, 0x0f, 0, 0, 0, 0x18, 0, 0, 0x16, 0x68}
	cancelHead = []byte{ // spotter_cancel.mp3: 48 кбит/с 32 кГц моно, Info: 17 кадров, 3888 байт
		0xff, 0xfb, 0x38, 0xc4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'I', 'n', 'f', 'o',
		0, 0, 0, 0x0f, 0, 0, 0, 0x11, 0, 0, 0x0f, 0x30}
	volumeHead = []byte{ // volume_sound.mp3: 48 кбит/с 44,1 кГц моно, заголовка Xing нет (сразу звук)
		0xff, 0xfb, 0x30, 0xc4, 0, 0, 0x06, 0xfc, 0x69, 0x14, 0x14, 0x94, 0x80, 0x01, 0x21, 0x1e,
		0x6e, 0xb7, 0x1e, 0xd0, 0x02, 0xf5, 0x0c, 0x5c, 0x56, 0x09, 0x93, 0xa4, 0x17, 0x04, 0xc1, 0x30, 0xd8, 0xac, 0x9d, 0x1c}
)

func id3(size int) []byte { // ID3v2 с телом заданного размера
	b := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	return append(b, make([]byte, size)...)
}

func TestMp3Bitrate(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want uint64
	}{
		{"настоящий timer.wav", timerHead, 64},
		{"MPEG1 128", []byte{0xff, 0xfb, 0x90, 0x00}, 128},
		{"MPEG2 64 (индекс 8)", []byte{0xff, 0xf3, 0x80, 0x00}, 64},
		{"после тега ID3", append(id3(300), timerHead...), 64},
		{"мусор перед кадром", append([]byte{1, 2, 0xff, 0x00, 3}, timerHead...), 64},
		{"layer II не берём", []byte{0xff, 0xfd, 0x90, 0x00}, 0},
		{"индекс 15 запрещён", []byte{0xff, 0xfb, 0xf0, 0x00}, 0},
		{"индекс 0 (free) не берём", []byte{0xff, 0xfb, 0x00, 0x00}, 0},
		{"не MP3", []byte("OggS\x00\x02" + string(make([]byte, 40))), 0},
		{"пусто", nil, 0},
		{"обрыв на заголовке", []byte{0xff, 0xfb, 0x50}, 0},
	}
	for _, c := range cases {
		if got := Bitrate(c.data); got != c.want {
			t.Errorf("%s: %d, ждали %d", c.name, got, c.want)
		}
	}
}

// TestAvgBitrateMatchesMediad: средний битрейт по Xing/Info совпадает с тем, что mediad показывал платформе в записях
// (diag bitrate у плеера файлов): 64, 80, 51 и «нет» у файла без Xing.
func TestAvgBitrateMatchesMediad(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want uint64
	}{
		{"timer.wav", timerHead, 64},
		{"spotter_start.mp3 (переменный битрейт: первый кадр 128)", spotterHead, 80},
		{"spotter_cancel.mp3", cancelHead, 51},
		{"volume_sound.mp3 без Xing", volumeHead, 0},
		{"после тега ID3", append(id3(100), spotterHead...), 80},
		{"заголовок обрезан", spotterHead[:30], 0},
		{"нет числа кадров", func() []byte { b := append([]byte(nil), timerHead...); b[28] = 0x0e; return b }(), 0},
		{"не MP3", []byte("OggS" + string(make([]byte, 60))), 0},
	}
	for _, c := range cases {
		if got := AvgBitrate(c.data); got != c.want {
			t.Errorf("%s: %d, ждали %d", c.name, got, c.want)
		}
	}
	// MPEG1 стерео: побочная информация 32 байта, Xing на смещении 36 (1000 кадров × 1152 / 44100 Гц, 417960 байт ≈ 128 кбит/с)
	st := append([]byte{0xff, 0xfb, 0x90, 0x00}, make([]byte, 32)...)
	st = append(st, 'X', 'i', 'n', 'g', 0, 0, 0, 3, 0, 0, 0x03, 0xe8, 0, 0x06, 0x60, 0xa8)
	if got := AvgBitrate(st); got != 128 {
		t.Errorf("стерео: %d, ждали 128", got)
	}
}

func TestFileBitrateSkipsHugeID3Tag(t *testing.T) {
	dir := t.TempDir()
	// Тег больше окна чтения (4096): кадр лежит дальше, и его надо найти переходом по размеру тега.
	size := 20000
	b := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	b = append(b, bytes.Repeat([]byte{0xff, 0x00}, size/2)...) // в теге попадаются «ложные синхрослова»
	b = append(b, timerHead...)
	path := filepath.Join(dir, "x.wav")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FileBitrate(path); got != 64 {
		t.Fatalf("FileBitrate = %d, ждали 64", got)
	}
	if FileBitrate(filepath.Join(dir, "нет.wav")) != 0 {
		t.Fatal("несуществующий файл должен дать 0")
	}
}
