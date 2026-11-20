package hls

import (
	"encoding/binary"
	"errors"
	"fmt"

	"openmediad/internal/libs/aac"
)

// Сегменты fMP4 (фрагментированный MP4, ISO/IEC 14496-12): так отдаёт звук SoundCloud. Поток — сегмент инициализации (ftyp + moov:
// чем сжат звук, AudioSpecificConfig) и фрагменты (moof + mdat: таблица размеров кадров и сами кадры). Кадры AAC отдаются
// с заголовком ADTS, как из MPEG-TS: декодеру всё равно, откуда они.
//
// Поддержано: одна звуковая дорожка AAC-LC (mp4a), размеры кадров в trun или по умолчанию (tfhd, trex), смещение данных от
// начала moof или явное. Не поддержано (ErrUnsupported): шифрование (enca, Common Encryption), другие кодеки.

// fmp4 — состояние разбора между сегментами.
type fmp4 struct {
	track       uint32 // номер звуковой дорожки
	asc         []byte // AudioSpecificConfig
	defaultSize uint32 // размер кадра по умолчанию из trex
}

// box — заголовок бокса MP4: тип, начало данных и конец бокса в буфере.
type box struct {
	typ              string
	start, body, end int
}

// boxes перечисляет боксы в b[from:to].
func boxes(b []byte, from, to int, f func(bx box) error) error {
	for off := from; off < to; {
		if to-off < 8 {
			return errors.New("hls: обрезанный бокс MP4")
		}
		size := int64(binary.BigEndian.Uint32(b[off:]))
		hdr := 8
		switch size {
		case 0: // до конца
			size = int64(to - off)
		case 1: // 64-битный размер
			if to-off < 16 {
				return errors.New("hls: обрезанный бокс MP4")
			}
			size, hdr = int64(binary.BigEndian.Uint64(b[off+8:])), 16
		}
		if size < int64(hdr) || size > int64(to-off) {
			return fmt.Errorf("hls: бокс MP4 %q не помещается в сегмент", string(b[off+4:off+8]))
		}
		bx := box{typ: string(b[off+4 : off+8]), start: off, body: off + hdr, end: off + int(size)}
		if err := f(bx); err != nil {
			return err
		}
		off = bx.end
	}
	return nil
}

// child находит первый вложенный бокс типа typ.
func child(b []byte, parent box, skip int, typ string) (box, bool) {
	var found box
	ok := false
	boxes(b, parent.body+skip, parent.end, func(bx box) error {
		if !ok && bx.typ == typ {
			found, ok = bx, true
		}
		return nil
	})
	return found, ok
}

// push разбирает сегмент fMP4 (с инициализацией в начале или без) и возвращает кадры ADTS.
func (m *fmp4) push(seg []byte) ([]byte, error) {
	var out []byte
	var sizes []uint32 // размеры кадров последнего trun, чьи данные ещё не взяты
	dataAt := -1       // где в буфере начинаются эти данные (-1 — с начала следующего mdat)
	take := func(at int) error {
		if len(sizes) == 0 {
			return nil
		}
		if m.asc == nil {
			return errors.New("hls: фрагмент fMP4 раньше сегмента инициализации")
		}
		for _, n := range sizes {
			if n == 0 || at+int(n) > len(seg) {
				return errors.New("hls: кадр fMP4 за пределами сегмента")
			}
			h, err := aac.ADTS(m.asc, int(n))
			if err != nil {
				return fmt.Errorf("%w: %v", ErrUnsupported, err)
			}
			out = append(out, h[:]...)
			out = append(out, seg[at:at+int(n)]...)
			at += int(n)
		}
		sizes = nil
		return nil
	}
	err := boxes(seg, 0, len(seg), func(bx box) error {
		switch bx.typ {
		case "moov":
			return m.readInit(seg, bx)
		case "moof":
			if m.asc == nil {
				return errors.New("hls: фрагмент fMP4 раньше сегмента инициализации")
			}
			var err error
			sizes, dataAt, err = m.readFragment(seg, bx)
			if err != nil {
				return err
			}
			if dataAt >= 0 {
				return take(dataAt)
			}
		case "mdat":
			if dataAt < 0 {
				return take(bx.body)
			}
		}
		return nil // ftyp, styp, sidx, free, prft, emsg …
	})
	return out, err
}

// readInit читает moov: звуковую дорожку, её AudioSpecificConfig и размер кадра по умолчанию.
func (m *fmp4) readInit(b []byte, moov box) error {
	m.asc, m.track, m.defaultSize = nil, 0, 0
	err := boxes(b, moov.body, moov.end, func(trak box) error {
		if trak.typ != "trak" || m.asc != nil {
			return nil
		}
		tkhd, ok := child(b, trak, 0, "tkhd")
		mdia, ok2 := child(b, trak, 0, "mdia")
		if !ok || !ok2 || tkhd.end-tkhd.body < 24 {
			return nil
		}
		hdlr, ok := child(b, mdia, 0, "hdlr")
		if !ok || hdlr.end-hdlr.body < 12 || string(b[hdlr.body+8:hdlr.body+12]) != "soun" {
			return nil // не звук
		}
		track := binary.BigEndian.Uint32(b[tkhd.body+12:]) // версия 0 и 1: перед номером 12 и 20 байт
		if b[tkhd.body] == 1 {
			if tkhd.end-tkhd.body < 24 {
				return nil
			}
			track = binary.BigEndian.Uint32(b[tkhd.body+20:])
		}
		var stsd box
		path := []string{"minf", "stbl", "stsd"}
		cur := mdia
		for _, name := range path {
			if cur, ok = child(b, cur, 0, name); !ok {
				return nil
			}
		}
		stsd = cur
		var entry box
		if entry, ok = firstEntry(b, stsd); !ok {
			return nil
		}
		switch entry.typ {
		case "mp4a":
		case "enca":
			return fmt.Errorf("%w: звук зашифрован (enca)", ErrUnsupported)
		default:
			return fmt.Errorf("%w: кодек дорожки %q, нужен AAC (mp4a)", ErrUnsupported, entry.typ)
		}
		esds, ok := child(b, entry, 28, "esds") // 28 байт — поля звукового описания перед вложенными боксами
		if !ok {
			return errors.New("hls: у дорожки mp4a нет esds")
		}
		asc, err := ascFromESDS(b[esds.body:esds.end])
		if err != nil {
			return err
		}
		m.asc, m.track = asc, track
		return nil
	})
	if err != nil {
		return err
	}
	if m.asc == nil {
		return fmt.Errorf("%w: в сегменте инициализации нет звуковой дорожки AAC", ErrUnsupported)
	}
	if mvex, ok := child(b, moov, 0, "mvex"); ok {
		boxes(b, mvex.body, mvex.end, func(trex box) error {
			if trex.typ == "trex" && trex.end-trex.body >= 24 && binary.BigEndian.Uint32(b[trex.body+4:]) == m.track {
				m.defaultSize = binary.BigEndian.Uint32(b[trex.body+16:])
			}
			return nil
		})
	}
	return nil
}

// firstEntry — первое описание дорожки в stsd (версия и флаги, число описаний, затем боксы).
func firstEntry(b []byte, stsd box) (box, bool) {
	if stsd.end-stsd.body < 8 {
		return box{}, false
	}
	var first box
	ok := false
	boxes(b, stsd.body+8, stsd.end, func(bx box) error {
		if !ok {
			first, ok = bx, true
		}
		return nil
	})
	return first, ok
}

// ascFromESDS достаёт AudioSpecificConfig из esds: ES_Descriptor (0x03) → DecoderConfigDescriptor (0x04) → DecoderSpecificInfo (0x05).
func ascFromESDS(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, errors.New("hls: короткий esds")
	}
	b = b[4:] // версия и флаги
	desc := func(want byte) ([]byte, bool) {
		for len(b) >= 2 {
			tag := b[0]
			n, i := 0, 1
			for ; i < 5 && i < len(b); i++ { // длина: до четырёх байт по 7 бит
				n = n<<7 | int(b[i]&0x7F)
				if b[i]&0x80 == 0 {
					break
				}
			}
			i++
			if i > len(b) || i+n > len(b) {
				return nil, false
			}
			body := b[i : i+n]
			if tag == want {
				return body, true
			}
			b = b[i+n:]
		}
		return nil, false
	}
	es, ok := desc(0x03)
	if !ok || len(es) < 3 {
		return nil, errors.New("hls: в esds нет ES_Descriptor")
	}
	flags := es[2]
	es = es[3:]
	if flags&0x80 != 0 { // зависимый поток
		es = es[min(2, len(es)):]
	}
	if flags&0x40 != 0 && len(es) > 0 { // URL
		es = es[min(1+int(es[0]), len(es)):]
	}
	if flags&0x20 != 0 { // OCR
		es = es[min(2, len(es)):]
	}
	b = es
	dc, ok := desc(0x04)
	if !ok || len(dc) < 13 {
		return nil, errors.New("hls: в esds нет DecoderConfigDescriptor")
	}
	if dc[0] != 0x40 {
		return nil, fmt.Errorf("%w: звук не MPEG-4 AAC (тип 0x%02x)", ErrUnsupported, dc[0])
	}
	b = dc[13:]
	asc, ok := desc(0x05)
	if !ok || len(asc) < 2 {
		return nil, errors.New("hls: в esds нет AudioSpecificConfig")
	}
	return append([]byte(nil), asc...), nil
}

// readFragment читает moof: размеры кадров звуковой дорожки и где в буфере их данные (-1 — с начала следующего mdat).
func (m *fmp4) readFragment(b []byte, moof box) (sizes []uint32, dataAt int, err error) {
	dataAt = -1
	err = boxes(b, moof.body, moof.end, func(traf box) error {
		if traf.typ != "traf" {
			return nil
		}
		tfhd, ok := child(b, traf, 0, "tfhd")
		if !ok || tfhd.end-tfhd.body < 8 {
			return errors.New("hls: у фрагмента fMP4 нет tfhd")
		}
		p := tfhd.body
		flags := uint32(b[p+1])<<16 | uint32(b[p+2])<<8 | uint32(b[p+3])
		if binary.BigEndian.Uint32(b[p+4:]) != m.track {
			return nil // чужая дорожка
		}
		if _, enc := child(b, traf, 0, "senc"); enc {
			return fmt.Errorf("%w: фрагменты зашифрованы (senc)", ErrUnsupported)
		}
		base := moof.start // смещение данных по умолчанию — от начала moof (default-base-is-moof; у звука без base_data_offset так и есть)
		def := m.defaultSize
		q := p + 8
		read := func(n int) uint64 {
			if q+n > tfhd.end {
				q = tfhd.end + 1
				return 0
			}
			var v uint64
			for i := 0; i < n; i++ {
				v = v<<8 | uint64(b[q+i])
			}
			q += n
			return v
		}
		if flags&0x01 != 0 {
			base = int(read(8))
		}
		if flags&0x02 != 0 {
			read(4)
		}
		if flags&0x08 != 0 {
			read(4)
		}
		if flags&0x10 != 0 {
			def = uint32(read(4))
		}
		if q > tfhd.end {
			return errors.New("hls: обрезанный tfhd")
		}
		return boxes(b, traf.body, traf.end, func(trun box) error {
			if trun.typ != "trun" {
				return nil
			}
			if len(sizes) > 0 {
				return fmt.Errorf("%w: несколько trun в одном фрагменте", ErrUnsupported)
			}
			t := trun.body
			if trun.end-t < 8 {
				return errors.New("hls: обрезанный trun")
			}
			tf := uint32(b[t+1])<<16 | uint32(b[t+2])<<8 | uint32(b[t+3])
			count := int(binary.BigEndian.Uint32(b[t+4:]))
			t += 8
			if tf&0x01 != 0 {
				if trun.end-t < 4 {
					return errors.New("hls: обрезанный trun")
				}
				dataAt = base + int(int32(binary.BigEndian.Uint32(b[t:])))
				t += 4
			}
			if tf&0x04 != 0 {
				t += 4
			}
			per := 0
			for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} {
				if tf&bit != 0 {
					per += 4
				}
			}
			if count < 0 || count > 1<<20 || t+count*per > trun.end {
				return errors.New("hls: обрезанный trun")
			}
			sizes = make([]uint32, count)
			for i := range sizes {
				s := t + i*per
				if tf&0x100 != 0 {
					s += 4
				}
				if tf&0x200 != 0 {
					sizes[i] = binary.BigEndian.Uint32(b[s:])
				} else {
					sizes[i] = def
				}
			}
			return nil
		})
	})
	if dataAt >= len(b) || dataAt < -1 {
		return nil, -1, errors.New("hls: данные фрагмента fMP4 вне сегмента")
	}
	return sizes, dataAt, err
}
