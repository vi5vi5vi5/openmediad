package clockled

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Штатные анимации плеера для дисплея (имена файлов без .binscled.zst). Для «продолжить» у прошивки анимации дисплея нет.
const (
	AnimPause    = "player_pause"
	AnimNext     = "player_next_track"
	AnimPrevious = "player_previous_track"
)

const (
	frameSize = 1 + Segments + 2 // флаг, яркость сегментов, длительность
	maxFrames = 4096
	maxDur    = 30 * time.Second
	maxFile   = 1 << 20
	zWindow   = 1 << 27 // самое большое окно zstd, которое принимаем
)

// Step — один кадр анимации.
type Step struct {
	Frame Frame
	Dur   time.Duration
}

// Anim — анимация дисплея.
type Anim struct {
	Steps []Step
	Loop  bool // зациклена (у анимаций Алисы; у плеера нет)
}

// ParseAnim разбирает распакованный поток кадров.
func ParseAnim(raw []byte) (*Anim, error) {
	if len(raw) == 0 || len(raw)%frameSize != 0 {
		return nil, fmt.Errorf("размер %d байт не делится на кадры по %d", len(raw), frameSize)
	}
	n := len(raw) / frameSize
	if n > maxFrames {
		return nil, fmt.Errorf("кадров %d, больше %d", n, maxFrames)
	}
	a := &Anim{Loop: raw[0]&1 != 0}
	for i := 0; i < n; i++ {
		b := raw[i*frameSize : (i+1)*frameSize]
		var st Step
		copy(st.Frame[:], b[1:1+Segments])
		st.Dur = time.Duration(uint16(b[1+Segments])<<8|uint16(b[2+Segments])) * time.Millisecond
		if st.Dur > maxDur {
			return nil, fmt.Errorf("кадр %d: длительность %v", i, st.Dur)
		}
		a.Steps = append(a.Steps, st)
	}
	return a, nil
}

var (
	zOnce sync.Once
	zDec  *zstd.Decoder
	zErr  error
)

// decompress распаковывает zstd (декодер общий на всех, потоков внутри ему не нужно).
func decompress(src []byte) ([]byte, error) {
	zOnce.Do(func() {
		// Файлы прошивки сжаты с окном 128 МБ (оно записано в заголовке), хотя сами крошечные. Декодер не даёт окну быть больше предела размера результата,
		// поэтому предел ставим по окну, а размер распакованного проверяем сами.
		zDec, zErr = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(zWindow), zstd.WithDecoderMaxMemory(zWindow))
	})
	if zErr != nil {
		return nil, zErr
	}
	out, err := zDec.DecodeAll(src, nil)
	if err == nil && len(out) > maxFile {
		return nil, fmt.Errorf("распаковано %d байт, больше %d", len(out), maxFile)
	}
	return out, err
}

// DecodeAnim распаковывает и разбирает файл .binscled.zst.
func DecodeAnim(compressed []byte) (*Anim, error) {
	raw, err := decompress(compressed)
	if err != nil {
		return nil, fmt.Errorf("zstd: %w", err)
	}
	return ParseAnim(raw)
}

// LoadAnim читает анимацию name (AnimPause …) из каталога dir (AnimDir на колонке).
func LoadAnim(dir, name string) (*Anim, error) {
	data, err := os.ReadFile(filepath.Join(dir, name+".binscled.zst"))
	if err != nil {
		return nil, err
	}
	a, err := DecodeAnim(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return a, nil
}

// Total — длительность анимации целиком.
func (a *Anim) Total() time.Duration {
	var t time.Duration
	for _, s := range a.Steps {
		t += s.Dur
	}
	return t
}

// Lit — до какого момента на дисплее что-то горит: чёрный хвост (у перемотки 0,5 с) нам ни к чему, дальше возвращается время. У зацикленной — вся
// длительность.
func (a *Anim) Lit() time.Duration {
	if a.Loop {
		return a.Total()
	}
	var t, lit time.Duration
	for _, s := range a.Steps {
		t += s.Dur
		if !s.Frame.IsZero() {
			lit = t
		}
	}
	return lit
}

// At — кадр на момент elapsed от начала; done — анимация закончилась.
func (a *Anim) At(elapsed time.Duration) (f Frame, done bool) {
	var t time.Duration
	for _, s := range a.Steps {
		t += s.Dur
		if elapsed < t {
			return s.Frame, false
		}
	}
	return Frame{}, true
}
