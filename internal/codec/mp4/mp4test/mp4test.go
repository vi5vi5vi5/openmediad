// Package mp4test — синтетические MP4 для тестов источников (как httptest для net/http).
package mp4test

import (
	"bytes"
	"encoding/binary"
)

func mk(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func u32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

// Synth собирает небольшой MP4 с AAC-LC 48 кГц стерео: n кадров по 1024, размеры size(i), два кадра в чанке.
func Synth(n int, size func(i int) uint32) (file []byte, payload [][]byte) {
	asc := []byte{0x11, 0x90} // AAC-LC, 48 кГц, стерео
	dsi := append([]byte{0x05, byte(len(asc))}, asc...)
	dcd := append([]byte{0x04, byte(13 + len(dsi)), 0x40, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, dsi...)
	sl := []byte{0x06, 1, 2}
	esd := append(append([]byte{0x03, byte(3 + len(dcd) + len(sl)), 0, 1, 0}, dcd...), sl...)
	esds := mk("esds", u32(0), esd)
	mp4a := mk("mp4a", make([]byte, 28), esds)
	stsd := mk("stsd", u32(0, 1), mp4a)
	stts := mk("stts", u32(0, 1, uint32(n), 1024))
	stsc := mk("stsc", u32(0, 1, 1, 2, 1))
	sizes := make([]uint32, n)
	for i := range sizes {
		sizes[i] = size(i)
	}
	stsz := mk("stsz", u32(0, 0, uint32(n)), u32(sizes...))
	chunks := (n + 1) / 2
	mdatStart := uint32(0)
	build := func(start uint32) []byte {
		offs := make([]uint32, 0, chunks)
		pos := start
		for i := 0; i < n; i++ {
			if i%2 == 0 {
				offs = append(offs, pos)
			}
			pos += sizes[i]
		}
		stco := mk("stco", u32(0, uint32(len(offs))), u32(offs...))
		stbl := mk("stbl", stsd, stts, stsc, stsz, stco)
		mdhd := mk("mdhd", u32(0, 0, 0, 48000, uint32(n*1024)), []byte{0, 0, 0, 0})
		hdlr := mk("hdlr", u32(0, 0), []byte("soun"), make([]byte, 12))
		mdia := mk("mdia", mdhd, hdlr, mk("minf", stbl))
		mvhd := mk("mvhd", u32(0, 0, 0, 1000, uint32(uint64(n)*1024*1000/48000)), make([]byte, 80))
		moov := mk("moov", mvhd, mk("trak", mdia))
		return append(mk("ftyp", []byte("isom"), u32(0)), moov...)
	}
	head := build(0)
	mdatStart = uint32(len(head)) + 8
	head = build(mdatStart)
	var data []byte
	for i := 0; i < n; i++ {
		p := bytes.Repeat([]byte{byte(i)}, int(sizes[i]))
		payload = append(payload, p)
		data = append(data, p...)
	}
	return append(head, mk("mdat", data)...), payload
}
