// Package aac — AAC для источников: заголовок ADTS для голых кадров из MP4 и декодер AAC → PCM.
//
// Декодер — GStreamer колонки (`maind --gst-launch`): чистого Go-декодера AAC в проекте нет (это запас З5 в PLAN.md, §13).
// Один процесс ≈12 МБ Pss и ≈18× быстрее реального времени (замер на колонке, docs/research/HISTORY.md, раздел 17).
package aac

import (
	"errors"
	"io"

	"openmediad/internal/codec/gst"
)

// Decoder — декодер AAC → PCM S16LE 48 кГц стерео: на вход кадры ADTS, на выход PCM.
type Decoder interface {
	io.Writer // кадры ADTS
	io.Reader // PCM
	// CloseWrite сообщает конец потока: декодер доработает остаток и закроет выход (io.EOF у Read).
	CloseWrite() error
	// Close прерывает работу и освобождает процесс.
	Close() error
}

// Factory создаёт новый декодер (по одному на сессию воспроизведения).
type Factory func() (Decoder, error)

// pipeline — конвейер декодирования: все элементы есть во вшитом GStreamer колонки (проверено на колонке).
var pipeline = "fdsrc fd=0 ! aacparse ! avdec_aac ! " + gst.RawOut(48000, 2)

type gstDecoder struct{ p *gst.Process }

// NewGst запускает декодер AAC на GStreamer колонки. Один процесс — одна сессия.
func NewGst() (Decoder, error) {
	p, err := gst.Start(pipeline, true)
	if err != nil {
		return nil, err
	}
	return &gstDecoder{p}, nil
}

func (d *gstDecoder) Write(b []byte) (int, error) { return d.p.In.Write(b) }
func (d *gstDecoder) Read(b []byte) (int, error)  { return d.p.Out.Read(b) }
func (d *gstDecoder) CloseWrite() error           { return d.p.In.Close() }
func (d *gstDecoder) Close() error                { return d.p.Close() }

// Tail — хвост журнала процесса (для сообщения «декодер не выдал звук»).
func (d *gstDecoder) Tail() string { return d.p.Tail() }

// ADTS строит заголовок ADTS для кадра AAC размером size байт по AudioSpecificConfig.
func ADTS(asc []byte, size int) ([7]byte, error) {
	var h [7]byte
	if len(asc) < 2 {
		return h, errors.New("adts: короткий AudioSpecificConfig")
	}
	obj := int(asc[0] >> 3)
	freq := int(asc[0]&7)<<1 | int(asc[1]>>7)
	ch := int(asc[1]>>3) & 0xf
	if obj == 0 || obj > 4 || freq > 12 || ch == 0 || ch > 7 {
		return h, errors.New("adts: профиль/частота/каналы вне поддержки (нужен AAC-LC)")
	}
	l := size + 7
	h[0] = 0xff
	h[1] = 0xf1 // MPEG-4, layer 0, без CRC
	h[2] = byte(obj-1)<<6 | byte(freq)<<2 | byte(ch>>2)
	h[3] = byte(ch&3)<<6 | byte(l>>11)&3
	h[4] = byte(l >> 3)
	h[5] = byte(l&7)<<5 | 0x1f
	h[6] = 0xfc
	return h, nil
}
