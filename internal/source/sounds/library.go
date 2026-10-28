package sounds

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/codec/gst"
	"openmediad/internal/codec/mp3"
)

const (
	// cacheLimit — сколько раскодированного звука держим для повторных звонков (≈30 с стерео).
	cacheLimit = 6 << 20
	// maxPCM — предел раскодированного файла (≈87 с стерео): длинный файл в памяти не держим, его играет mediad.
	maxPCM = 16 << 20
	// decodeTimeout — сторож раскодирования: GStreamer колонки на неверном конвейере не завершается сам.
	decodeTimeout = 20 * time.Second
)

// Clip — раскодированный звук.
type Clip struct {
	PCM     []byte // S16LE 48 кГц стерео, длина кратна 4
	Bitrate uint64 // кбит/с у MP3; 0 — не MP3 или неизвестно
}

// Duration — длительность одного прохода.
func (c *Clip) Duration() time.Duration { return audio.Stereo48.Duration(uint64(len(c.PCM))) }

// Library — раскодированные звуки с кэшем. Нулевое значение готово к работе.
type Library struct {
	// Decode (необязательный) подменяет раскодирование в тестах; по умолчанию DecodeFile.
	Decode func(path string) (*Clip, error)

	mu    sync.Mutex
	cache map[string]*Clip
	order []string
	bytes int
}

// Clip возвращает звук файла: из кэша или раскодированный сейчас.
func (l *Library) Clip(path string) (*Clip, error) {
	l.mu.Lock()
	c := l.cache[path]
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	dec := l.Decode
	if dec == nil {
		dec = DecodeFile
	}
	c, err := dec(path)
	if err != nil {
		return nil, err
	}
	if len(c.PCM) == 0 {
		return nil, fmt.Errorf("в %s нет звука", path)
	}
	l.mu.Lock()
	l.remember(path, c)
	l.mu.Unlock()
	return c, nil
}

// Preload раскодирует файл заранее: первый звонок тогда начнётся без запуска GStreamer (≈17 мс до звука вместо ≈0,5 с).
func (l *Library) Preload(path string) error {
	_, err := l.Clip(path)
	return err
}

// remember кладёт звук в кэш, вытесняя самые старые, пока не уложимся в cacheLimit. Звук больше лимита в кэш не идёт.
// Вызывать под l.mu.
func (l *Library) remember(path string, c *Clip) {
	if len(c.PCM) > cacheLimit || l.cache[path] != nil {
		return
	}
	if l.cache == nil {
		l.cache = map[string]*Clip{}
	}
	for l.bytes+len(c.PCM) > cacheLimit && len(l.order) > 0 {
		old := l.order[0]
		l.order = l.order[1:]
		l.bytes -= len(l.cache[old].PCM)
		delete(l.cache, old)
	}
	l.cache[path] = c
	l.order = append(l.order, path)
	l.bytes += len(c.PCM)
}

// DecodeFile раскодирует файл в PCM S16LE 48 кГц стерео через GStreamer колонки (`filesrc ! decodebin ! …`). Проверено на
// колонке: timer.wav (MP3 под именем .wav) даёт 1 780 504 байт ≈ 9,27 с за ≈0,5 с.
func DecodeFile(path string) (*Clip, error) {
	if !ValidPath(path) {
		return nil, fmt.Errorf("недопустимый путь %q", path)
	}
	p, err := gst.Start("filesrc location="+path+" ! decodebin ! "+gst.RawOut(audio.Rate, 2), false)
	if err != nil {
		return nil, err
	}
	// Зависший или слишком длинный разбор прерываем: это GStreamer, а не aplay, SIGKILL на смеситель звука не влияет.
	timer := time.AfterFunc(decodeTimeout, p.Kill)
	var buf bytes.Buffer
	buf.Grow(2 << 20)
	n, rerr := io.Copy(&buf, io.LimitReader(p.Out, maxPCM+1))
	timer.Stop()
	tooLong := n > maxPCM
	if rerr != nil || tooLong {
		p.Kill()
	}
	werr := p.Wait()
	p.Close()
	switch {
	case tooLong:
		return nil, fmt.Errorf("звук длиннее %d МБ", maxPCM>>20)
	case rerr != nil:
		return nil, rerr
	case werr != nil:
		return nil, werr
	}
	pcm := buf.Bytes()
	pcm = pcm[:len(pcm)&^3] // целое число стереокадров
	return &Clip{PCM: pcm, Bitrate: mp3.FileBitrate(path)}, nil
}
