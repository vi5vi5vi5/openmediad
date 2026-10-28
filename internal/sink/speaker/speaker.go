// Package speaker — выход «динамики колонки»: системный 32-битный aplay на ALSA default (общий смеситель dmix).
//
// На каждый звучащий поток — свой aplay: смешивает их смеситель колонки (проверено: до 4 клиентов вместе со штатной
// музыкой, docs/research/HISTORY.md, раздел 13). aplay живёт, только пока поток звучит.
package speaker

import (
	"sync"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/sink/alsa"
)

// Sink — динамики колонки.
type Sink struct {
	Aplay  string // путь к aplay; пусто — /usr/bin/aplay
	Device string // устройство ALSA; пусто — default
}

// Name — имя выхода в маршрутах настроек.
func (s *Sink) Name() string { return "speaker" }

// Available — динамики есть всегда.
func (s *Sink) Available() bool { return true }

// Open запускает aplay под формат потока.
func (s *Sink) Open(f audio.Format) (audio.Output, error) {
	p, err := alsa.Start(alsa.Config{Path: s.Aplay, Device: s.Device, Rate: f.Rate, Channels: f.Channels})
	if err != nil {
		return nil, err
	}
	return &Output{p: p}, nil
}

// Output — один запущенный aplay.
type Output struct {
	p     *alsa.Player
	abort sync.Once
}

// Write передаёт PCM aplay; блокируется, когда труба и буфер PCM заполнены — это и есть темп воспроизведения.
func (o *Output) Write(b []byte) (int, error) { return o.p.Write(b) }

// Drain закрывает вход aplay и ждёт, пока он доиграет остаток (не дольше 5 с, потом SIGINT).
func (o *Output) Drain() { o.p.Finish(5 * time.Second) }

// Abort обрывает aplay сигналом SIGINT (не SIGKILL: см. internal/sink/alsa). Повторный вызов ничего не делает.
func (o *Output) Abort() { o.abort.Do(func() { o.p.Abort(2 * time.Second) }) }
