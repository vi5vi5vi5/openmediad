package alsa

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Output — запущенный aplay как вывод движка (audio.Output). Общий для всех выходов через ALSA: динамики, наушники.
type Output struct {
	p     *Player
	abort sync.Once
}

// NewOutput оборачивает запущенный aplay.
func NewOutput(p *Player) *Output { return &Output{p: p} }

// Write передаёт PCM aplay; блокируется, когда труба и буфер PCM заполнены — это и есть темп воспроизведения. Если aplay
// уже вышел (наушники отключились, устройство пропало), в ошибке — последние строки его stderr.
func (o *Output) Write(b []byte) (int, error) {
	n, err := o.p.Write(b)
	if err != nil {
		select {
		case <-o.p.Done():
			if s := strings.TrimSpace(o.p.Stderr()); s != "" {
				err = fmt.Errorf("%w (aplay: %s)", err, lastLine(s))
			}
		default:
		}
	}
	return n, err
}

// Drain закрывает вход aplay и ждёт, пока он доиграет остаток (не дольше 5 с, потом SIGINT).
func (o *Output) Drain() { o.p.Finish(5 * time.Second) }

// Abort обрывает aplay сигналом SIGINT (не SIGKILL). Повторный вызов ничего не делает.
func (o *Output) Abort() { o.abort.Do(func() { o.p.Abort(2 * time.Second) }) }

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
