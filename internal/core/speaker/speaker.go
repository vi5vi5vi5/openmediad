// Package speaker — выход «динамики колонки»: системный 32-битный aplay на ALSA default (общий смеситель dmix).
//
// На каждый звучащий поток — свой aplay: смешивает их смеситель колонки (проверено: до 4 клиентов вместе со штатной
// музыкой, docs/research/HISTORY.md, раздел 13). aplay живёт, только пока поток звучит.
package speaker

import (
	"openmediad/internal/core/audio"
	"openmediad/internal/core/speaker/aplay"
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
	p, err := aplay.Start(aplay.Config{Path: s.Aplay, Device: s.Device, Rate: f.Rate, Channels: f.Channels})
	if err != nil {
		return nil, err
	}
	return aplay.NewOutput(p), nil
}
