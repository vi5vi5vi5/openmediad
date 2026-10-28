// Package audio — общие кирпичики звука: формат PCM, кольцевой буфер, громкость, нарастание. И типы-договор между
// движком (internal/engine), источниками (internal/source/*) и выходами (internal/sink/*): Info, Output, ErrWake. Пакет ни
// от чего своего не зависит, поэтому его могут импортировать все.
//
// Внутри движка звук всегда S16LE (знаковые 16 бит, младший байт первым); частота обычно 48 кГц — как у смесителя колонки.
package audio

import (
	"errors"
	"time"
)

// Rate — частота смесителя колонки (dmix_speakers): всё, что идёт на динамики, приводится к ней.
const Rate = 48000

// Format — формат PCM S16LE.
type Format struct {
	Rate     int // Гц
	Channels int // 1 или 2
}

// Stereo48 и Mono48 — форматы, в которых работают источники колонки.
var (
	Stereo48 = Format{Rate: Rate, Channels: 2}
	Mono48   = Format{Rate: Rate, Channels: 1}
)

// FrameBytes — байт на один отсчёт всех каналов.
func (f Format) FrameBytes() int { return 2 * f.Channels }

// BytesPerSec — байт на секунду звука.
func (f Format) BytesPerSec() int { return f.Rate * f.FrameBytes() }

// Bytes — сколько байт занимает d звука (целое число отсчётов).
func (f Format) Bytes(d time.Duration) int {
	return int(int64(d) * int64(f.Rate) / int64(time.Second) * int64(f.FrameBytes()))
}

// Duration — длительность n байт звука.
func (f Format) Duration(n uint64) time.Duration {
	if f.Rate == 0 || f.Channels == 0 {
		return 0
	}
	return time.Duration(n / uint64(f.FrameBytes()) * uint64(time.Second) / uint64(f.Rate))
}

// Info — что источник знает о звуке после открытия.
type Info struct {
	Start    time.Duration // позиция первого отсчёта (после открытия с середины или перемотки — начало кадра)
	Duration time.Duration // длительность; 0 — неизвестна (живой поток) или неважна
	Bitrate  int           // кбит/с исходного файла; 0 — неизвестен
}

// Output — открытый вывод одного потока (например, один запущенный aplay).
type Output interface {
	// Write передаёт PCM; блокируется в темпе воспроизведения. Ошибка — вывод больше не принимает звук.
	Write(p []byte) (int, error)
	// Drain доигрывает остаток и закрывает вывод.
	Drain()
	// Abort обрывает вывод сразу. Можно вызывать несколько раз и из другой горутины, пока идёт Write (тогда Write вернёт
	// ошибку). Для aplay это SIGINT, а не SIGKILL: оборванный посреди записи aplay может испортить общий смеситель.
	Abort()
}

// ErrWake — чтение прервано по просьбе движка (пауза, перемотка): данные не потеряны, можно читать дальше.
var ErrWake = errors.New("audio: чтение прервано")

// ErrStopped — буфер брошен (источник закрыт или перемотан): этих данных больше не будет.
var ErrStopped = errors.New("audio: буфер остановлен")
