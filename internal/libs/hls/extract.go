package hls

import (
	"errors"
	"fmt"
)

// Codec — чем сжат звук потока.
type Codec int

const (
	CodecUnknown Codec = iota
	CodecAAC           // AAC в кадрах ADTS
	CodecMP3           // MPEG-1/2 Audio Layer I–III
)

func (c Codec) String() string {
	switch c {
	case CodecAAC:
		return "AAC"
	case CodecMP3:
		return "MP3"
	}
	return "неизвестный"
}

// Extractor достаёт звук из сегментов потока: из MPEG-TS (обычный случай), из fMP4 (fmp4.go) или из «голых» сегментов AAC
// и MP3 с меткой ID3 в начале. На выходе — непрерывный элементарный поток (кадры ADTS или MP3), годный декодеру как есть.
// Состояние (номера потоков TS) живёт между сегментами: сегменты подаются по порядку.
type Extractor struct {
	codec    Codec
	raw      bool  // сегменты — голый звук, не MPEG-TS
	mp4      *fmp4 // сегменты fMP4
	decided  bool  // вид сегментов определён по первому
	pmtPID   int
	audioPID int
	started  bool // для звукового потока TS уже встречалось начало пакета PES
}

// Codec — кодек потока; известен после первого сегмента.
func (e *Extractor) Codec() Codec { return e.codec }

const tsPacket = 188

// Push разбирает очередной сегмент и возвращает звук из него.
func (e *Extractor) Push(seg []byte) ([]byte, error) {
	if !e.decided {
		switch {
		case len(seg) >= 2*tsPacket && seg[0] == 0x47 && seg[tsPacket] == 0x47:
		case len(seg) >= 8 && isMP4Box(string(seg[4:8])):
			e.mp4 = &fmp4{}
			e.codec = CodecAAC
		default:
			e.raw = true
		}
		e.decided = true
		e.pmtPID, e.audioPID = -1, -1
	}
	if e.mp4 != nil {
		return e.mp4.push(seg)
	}
	if e.raw {
		return e.pushRaw(seg)
	}
	return e.pushTS(seg)
}

func isMP4Box(typ string) bool {
	switch typ {
	case "ftyp", "styp", "moov", "moof", "sidx":
		return true
	}
	return false
}

// pushRaw — сегмент без контейнера: метка ID3v2 (в ней время сегмента) и кадры звука.
func (e *Extractor) pushRaw(seg []byte) ([]byte, error) {
	for len(seg) >= 10 && string(seg[:3]) == "ID3" {
		size := int(seg[6]&0x7F)<<21 | int(seg[7]&0x7F)<<14 | int(seg[8]&0x7F)<<7 | int(seg[9]&0x7F)
		if 10+size > len(seg) {
			return nil, errors.New("hls: метка ID3 длиннее сегмента")
		}
		seg = seg[10+size:]
	}
	if e.codec == CodecUnknown {
		if len(seg) < 2 || seg[0] != 0xFF || seg[1]&0xE0 != 0xE0 {
			return nil, fmt.Errorf("%w: в сегменте не MPEG-TS, не AAC и не MP3", ErrUnsupported)
		}
		if seg[1]&0xF6 == 0xF0 { // синхрослово ADTS: слой 00
			e.codec = CodecAAC
		} else {
			e.codec = CodecMP3
		}
	}
	return seg, nil
}

// pushTS — сегмент MPEG-TS: таблицы PAT и PMT говорят, в каком потоке звук; из его пакетов PES собирается элементарный поток.
func (e *Extractor) pushTS(seg []byte) ([]byte, error) {
	out := make([]byte, 0, len(seg))
	for off := 0; off+tsPacket <= len(seg); off += tsPacket {
		p := seg[off : off+tsPacket]
		if p[0] != 0x47 {
			return nil, errors.New("hls: сбой синхронизации MPEG-TS")
		}
		pid := int(p[1]&0x1F)<<8 | int(p[2])
		start := p[1]&0x40 != 0
		body := p[4:]
		if p[3]&0x20 != 0 { // поле адаптации
			if n := int(body[0]) + 1; n < len(body) {
				body = body[n:]
			} else {
				body = nil
			}
		}
		if p[3]&0x10 == 0 || len(body) == 0 { // пакет без данных
			continue
		}
		switch {
		case pid == 0 && start:
			e.readPAT(body)
		case pid == e.pmtPID && start:
			if err := e.readPMT(body); err != nil {
				return nil, err
			}
		case pid == e.audioPID:
			if start { // начало пакета PES: 00 00 01, поток, длина, флаги, длина заголовка
				if len(body) < 9 || body[0] != 0 || body[1] != 0 || body[2] != 1 {
					continue
				}
				hdr := 9 + int(body[8])
				if hdr > len(body) {
					continue
				}
				body, e.started = body[hdr:], true
			}
			if e.started {
				out = append(out, body...)
			}
		}
	}
	if e.audioPID < 0 {
		return nil, fmt.Errorf("%w: в MPEG-TS не найден звук AAC или MP3", ErrUnsupported)
	}
	return out, nil
}

// section возвращает секцию таблицы из пакета, начинающего её (после указателя).
func section(body []byte) []byte {
	if len(body) < 1 || 1+int(body[0]) >= len(body) {
		return nil
	}
	s := body[1+int(body[0]):]
	if len(s) < 3 {
		return nil
	}
	if n := 3 + (int(s[1]&0x0F)<<8 | int(s[2])); n <= len(s) {
		return s[:n]
	}
	return s // секция длиннее пакета: у таблиц звукового потока так не бывает, читаем что есть
}

func (e *Extractor) readPAT(body []byte) {
	s := section(body)
	if len(s) < 12 || s[0] != 0x00 {
		return
	}
	for i := 8; i+4 <= len(s)-4; i += 4 {
		if program := int(s[i])<<8 | int(s[i+1]); program != 0 {
			e.pmtPID = int(s[i+2]&0x1F)<<8 | int(s[i+3])
			return
		}
	}
}

func (e *Extractor) readPMT(body []byte) error {
	s := section(body)
	if len(s) < 16 || s[0] != 0x02 {
		return nil
	}
	i := 12 + (int(s[10]&0x0F)<<8 | int(s[11]))
	for i+5 <= len(s)-4 {
		typ := s[i]
		pid := int(s[i+1]&0x1F)<<8 | int(s[i+2])
		var codec Codec
		switch typ {
		case 0x0F:
			codec = CodecAAC
		case 0x03, 0x04:
			codec = CodecMP3
		}
		if codec != CodecUnknown {
			if e.codec != CodecUnknown && e.codec != codec {
				return fmt.Errorf("%w: кодек звука сменился посреди потока", ErrUnsupported)
			}
			if e.audioPID != pid {
				e.audioPID, e.started = pid, false
			}
			e.codec = codec
			return nil
		}
		i += 5 + (int(s[i+3]&0x0F)<<8 | int(s[i+4]))
	}
	return nil
}
