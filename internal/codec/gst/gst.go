// Package gst — запуск встроенного GStreamer колонки (`maind --gst-launch`) для декодирования: AAC потоков Яндекса, MP3
// звонков и всего, что умеет decodebin прошивки.
//
// Что надо знать о GStreamer колонки (docs/research/HISTORY.md, разделы 13–17):
//   - есть fdsrc fdsink filesrc filesink decodebin qtdemux aacparse avdec_aac mpegaudioparse avdec_mp3 souphttpsrc queue
//     typefind audioconvert audioresample; нет audiotestsrc, faad, mpg123audiodec;
//   - stdout и stderr maind заняты его журналом, поэтому PCM забирается через третий дескриптор: `fdsink fd=3`;
//   - на неверном конвейере процесс НЕ завершается и висит с ≈30 МБ: запускать только со сторожем по времени;
//   - SIGKILL ему допустим (общий смеситель звука он не держит, в отличие от aplay).
package gst

import (
	"sync"
)

// MaindPath — штатный maind колонки, в котором живёт GStreamer.
const MaindPath = "/system/vendor/quasar/maind"

// RawOut — хвост конвейера: PCM S16LE нужной частоты и числа каналов в третий дескриптор, без синхронизации по часам
// (темп задаёт тот, кто читает).
func RawOut(rate, channels int) string {
	return "audioconvert ! audioresample ! audio/x-raw,rate=" + itoa(rate) + ",channels=" + itoa(channels) +
		",format=S16LE ! fdsink fd=3 sync=false"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// tail хранит последние ~2 КБ вывода процесса (для сообщения об ошибке).
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
