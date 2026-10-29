// Package mp3 — сведения о файлах MPEG audio layer III: заголовок первого кадра и средний битрейт по заголовку Xing/Info.
// mediad показывает средний битрейт платформе в диагностике событий плеера файлов. Само декодирование MP3 — через GStreamer
// колонки (libs/gstreamer).
package mp3

import (
	"encoding/binary"
	"io"
	"os"
)

// Таблицы битрейтов MPEG audio layer III (кбит/с) по индексу из заголовка кадра.
var (
	mpeg1L3 = [16]uint64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3 = [16]uint64{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
)

// Частоты дискретизации MPEG1 (Гц) по индексу; у MPEG2 вдвое, у MPEG2.5 вчетверо меньше.
var mpeg1Rates = [4]uint64{44100, 48000, 32000, 0}

// frame — заголовок кадра MPEG layer III.
type frame struct {
	at      int    // смещение заголовка в данных
	mpeg1   bool   // MPEG1 (иначе MPEG2 или MPEG2.5)
	bitrate uint64 // кбит/с
	rate    uint64 // Гц
	mono    bool
}

// firstFrame ищет первый заголовок кадра layer III. Тег ID3v2 в начале пропускается по его размеру (если он целиком в data).
func firstFrame(data []byte) (frame, bool) {
	i := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		i = 10 + id3Size(data[6:10])
	}
	for ; i+4 <= len(data); i++ {
		if data[i] != 0xFF || data[i+1]&0xE0 != 0xE0 {
			continue
		}
		ver := (data[i+1] >> 3) & 3   // 3 — MPEG1, 2 — MPEG2, 0 — MPEG2.5, 1 — зарезервировано
		layer := (data[i+1] >> 1) & 3 // 1 — layer III
		idx := data[i+2] >> 4
		ri := (data[i+2] >> 2) & 3
		if ver == 1 || layer != 1 || idx == 0 || idx == 15 || ri == 3 {
			continue
		}
		f := frame{at: i, mpeg1: ver == 3, rate: mpeg1Rates[ri], mono: data[i+3]>>6 == 3}
		switch ver {
		case 3:
			f.bitrate = mpeg1L3[idx]
		case 2:
			f.bitrate, f.rate = mpeg2L3[idx], f.rate/2
		default:
			f.bitrate, f.rate = mpeg2L3[idx], f.rate/4
		}
		return f, true
	}
	return frame{}, false
}

// Bitrate — битрейт первого кадра MPEG layer III в data, кбит/с; 0, если кадра нет.
func Bitrate(data []byte) uint64 {
	f, _ := firstFrame(data)
	return f.bitrate
}

// AvgBitrate — средний битрейт по заголовку Xing/Info первого кадра, кбит/с с округлением: байты файла × 8 / длительность
// (кадры × отсчётов на кадр / частота). Так его считает разборщик MPEG в GStreamer, и это число mediad показывает платформе
// [проверено по записям 28.10.2026]: timer.wav — 64 (Info, постоянный битрейт), spotter_start.mp3 — 80 (Xing, переменный:
// первый кадр 128), spotter_cancel.mp3 — 51 (Info с кадрами по 48). Без заголовка Xing/Info — 0: у volume_sound.mp3 (4 кадра)
// mediad битрейта не показывает [вывод: у длинных файлов без Xing GStreamer, возможно, сообщает битрейт позже — не проверено].
func AvgBitrate(data []byte) uint64 {
	f, ok := firstFrame(data)
	if !ok {
		return 0
	}
	// заголовок Xing лежит сразу за побочной информацией первого кадра; её размер зависит от версии и числа каналов
	side := 32
	switch {
	case f.mpeg1 && f.mono, !f.mpeg1 && !f.mono:
		side = 17
	case !f.mpeg1 && f.mono:
		side = 9
	}
	x := f.at + 4 + side
	if x+16 > len(data) || (string(data[x:x+4]) != "Xing" && string(data[x:x+4]) != "Info") {
		return 0
	}
	flags := binary.BigEndian.Uint32(data[x+4:])
	if flags&3 != 3 { // нужны оба поля: число кадров и размер в байтах
		return 0
	}
	frames := uint64(binary.BigEndian.Uint32(data[x+8:]))
	size := uint64(binary.BigEndian.Uint32(data[x+12:]))
	spf := uint64(1152)
	if !f.mpeg1 {
		spf = 576
	}
	if frames == 0 || f.rate == 0 {
		return 0
	}
	bits := size * 8 * f.rate // бит × Гц; делим на отсчёты, получаем бит/с
	samples := frames * spf
	return (bits*2/samples/1000 + 1) / 2 // кбит/с с округлением до целого
}

// id3Size — размер тега ID3v2 без заголовка: четыре байта по 7 бит.
func id3Size(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

// FileBitrate читает начало файла и возвращает средний битрейт MP3 по заголовку Xing/Info (0 — не MP3, заголовка нет или
// не удалось прочитать).
func FileBitrate(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	head := make([]byte, 10)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0
	}
	skip := int64(0)
	if string(head[:3]) == "ID3" {
		skip = int64(10 + id3Size(head[6:10]))
	}
	if _, err := f.Seek(skip, io.SeekStart); err != nil {
		return 0
	}
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	return AvgBitrate(buf[:n])
}
