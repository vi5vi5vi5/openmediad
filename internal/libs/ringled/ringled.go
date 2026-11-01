// Package ringled — кольцо подсветки логотипа Станции Мини 3: шесть RGB-зон (/dev/fb0) и умение удерживать на нём свой кадр, когда
// прошивка рисует поверх.
//
// Железо: framebuffer `logo` 6×1 пикселей, 3 байта на пиксель в порядке BGR, запись — один WriteAt на 18 байт.
// Прошивка про нас не знает и пишет в то же устройство: на любое изменение состояния SDK (Алиса слушает, сменился трек …) она проигрывает свою анимацию
// и заканчивает «idle.led» — всё погашено. Поэтому Keeper каждые 20 мс сверяет кольцо с тем, что записал сам: если там другое, рисует прошивка;
// ждёт по её логу (maind.log), когда она вернётся в покой, и рисует своё заново. Разведка и замеры — в docs/leds-research.md эксперимента ledlab.
package ringled

import (
	"fmt"
	"os"

	"openmediad/internal/core/led"
)

// Zones — число RGB-зон кольца.
const Zones = 6

// FBPath — устройство кольца на Мини 3.
const FBPath = "/dev/fb0"

// Frame — цвета зон 0…5.
type Frame [Zones]led.Color

// IsZero — ни одна зона не горит.
func (f Frame) IsZero() bool { return f == Frame{} }

// Device — кольцо.
type Device interface {
	Read() (Frame, error)
	Write(Frame) error
}

// FB — кольцо через framebuffer.
type FB struct{ f *os.File }

// OpenFB открывает кольцо.
func OpenFB(path string) (*FB, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &FB{f}, nil
}

func (r *FB) Close() error { return r.f.Close() }

// Read возвращает кадр, который горит сейчас (его мог нарисовать кто угодно).
func (r *FB) Read() (Frame, error) {
	var buf [Zones * 3]byte
	if _, err := r.f.ReadAt(buf[:], 0); err != nil {
		return Frame{}, err
	}
	var fr Frame
	for i := range fr {
		fr[i] = led.Color{R: buf[i*3+2], G: buf[i*3+1], B: buf[i*3]}
	}
	return fr, nil
}

// Write рисует кадр (порядок байт BGR).
func (r *FB) Write(fr Frame) error {
	var buf [Zones * 3]byte
	for i, c := range fr {
		buf[i*3], buf[i*3+1], buf[i*3+2] = c.B, c.G, c.R
	}
	_, err := r.f.WriteAt(buf[:], 0)
	if err != nil {
		return fmt.Errorf("запись в %s: %w", r.f.Name(), err)
	}
	return nil
}
