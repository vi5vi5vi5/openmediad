package hls

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- fMP4 для тестов: сегмент инициализации и фрагменты, как у SoundCloud (AAC-LC, один trun на фрагмент) ----

func mp4box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

func fullbox(typ string, ver byte, flags uint32, parts ...[]byte) []byte {
	return mp4box(typ, append([]byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}, bytes.Join(parts, nil)...))
}

func u32(vs ...uint32) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.BigEndian.AppendUint32(b, v)
	}
	return b
}

// ascLC44 — AudioSpecificConfig: AAC-LC, 44,1 кГц, стерео.
var ascLC44 = []byte{0x12, 0x10}

// esdsOf собирает esds с AudioSpecificConfig.
func esdsOf(asc []byte) []byte {
	dsi := append([]byte{0x05, byte(len(asc))}, asc...)
	dc := append([]byte{0x04, byte(13 + len(dsi)), 0x40, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, dsi...)
	sl := []byte{0x06, 0x01, 0x02}
	es := append(append([]byte{0x03, 0x80, 0x80, byte(3 + len(dc) + len(sl)), 0x00, 0x01, 0x00}, dc...), sl...) // длина в несколько байт, как пишут кодировщики
	return fullbox("esds", 0, 0, es)
}

// initSegment — ftyp + moov с одной звуковой дорожкой track (entry — тип описания: mp4a, enca …).
func initSegment(track uint32, entry string, defaultSize uint32) []byte {
	tkhd := fullbox("tkhd", 0, 3, u32(0, 0, track, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	hdlr := fullbox("hdlr", 0, 0, u32(0), []byte("soun"), u32(0, 0, 0), []byte("SoundHandler\x00"))
	audio := append(make([]byte, 6), 0, 1)                           // reserved, data_reference_index
	audio = append(audio, make([]byte, 8)...)                        // reserved
	audio = append(audio, 0, 2, 0, 16, 0, 0, 0, 0, 0xAC, 0x44, 0, 0) // каналы, разрядность, reserved, частота 16.16
	sampleEntry := mp4box(entry, audio, esdsOf(ascLC44))
	stsd := fullbox("stsd", 0, 0, u32(1), sampleEntry)
	stbl := mp4box("stbl", stsd, fullbox("stts", 0, 0, u32(0)), fullbox("stsc", 0, 0, u32(0)), fullbox("stsz", 0, 0, u32(0, 0)), fullbox("stco", 0, 0, u32(0)))
	minf := mp4box("minf", fullbox("smhd", 0, 0, u32(0)), stbl)
	mdia := mp4box("mdia", fullbox("mdhd", 0, 0, u32(0, 0, 44100, 0, 0)), hdlr, minf)
	trak := mp4box("trak", tkhd, mdia)
	mvex := mp4box("mvex", fullbox("trex", 0, 0, u32(track, 1, 1024, defaultSize, 0)))
	moov := mp4box("moov", fullbox("mvhd", 0, 0, make([]byte, 96)), trak, mvex)
	return append(mp4box("ftyp", []byte("iso6"), u32(0), []byte("iso6mp41")), moov...)
}

// frames — кадры AAC разной длины с различимым содержимым.
func aacFrames(n int, mark byte) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = bytes.Repeat([]byte{mark + byte(i)}, 100+i*7)
	}
	return out
}

// fragment — moof + mdat. explicit — размеры кадров и смещение данных в trun (как у SoundCloud); иначе размер по умолчанию из trex
// и данные с начала mdat (тогда все кадры должны быть одной длины).
func fragment(track uint32, fs [][]byte, explicit bool) []byte {
	var trun []byte
	if explicit {
		sizes := u32(uint32(len(fs)), 0) // число кадров, смещение (заполним ниже)
		for _, f := range fs {
			sizes = append(sizes, u32(uint32(len(f)))...)
		}
		trun = fullbox("trun", 0, 0x201, sizes)
	} else {
		trun = fullbox("trun", 0, 0, u32(uint32(len(fs))))
	}
	tfhd := fullbox("tfhd", 0, 0x020000, u32(track))
	traf := mp4box("traf", tfhd, fullbox("tfdt", 1, 0, make([]byte, 8)), trun)
	moof := mp4box("moof", fullbox("mfhd", 0, 0, u32(1)), traf)
	if explicit { // смещение данных от начала moof: сам moof и заголовок mdat
		off := bytes.Index(moof, []byte("trun")) + 4 + 4 + 4
		binary.BigEndian.PutUint32(moof[off:], uint32(len(moof)+8))
	}
	return append(moof, mp4box("mdat", bytes.Join(fs, nil))...)
}

// adtsPayloads разбирает цепочку ADTS и возвращает данные кадров (без заголовков).
func adtsPayloads(t *testing.T, b []byte) [][]byte {
	t.Helper()
	if _, err := checkADTS(b); err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for len(b) > 0 {
		l := int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5])>>5
		if b[2] != 0x50 || b[3]&0xC0 != 0x80 { // профиль LC, 44,1 кГц, стерео — из AudioSpecificConfig
			t.Fatalf("заголовок ADTS % x не по AudioSpecificConfig", b[:7])
		}
		out = append(out, b[7:l])
		b = b[l:]
	}
	return out
}

func TestExtractFMP4(t *testing.T) {
	f1, f2 := aacFrames(5, 0x10), aacFrames(4, 0x40)
	var e Extractor
	es, err := e.Push(append(initSegment(1, "mp4a", 0), fragment(1, f1, true)...))
	if err != nil || e.Codec() != CodecAAC {
		t.Fatalf("первый сегмент: %v, кодек %v", err, e.Codec())
	}
	es2, err := e.Push(append(mp4box("styp", []byte("msdh")), fragment(1, f2, true)...))
	if err != nil {
		t.Fatal(err)
	}
	got := adtsPayloads(t, append(es, es2...))
	want := append(f1, f2...)
	if len(got) != len(want) {
		t.Fatalf("кадров %d, ждали %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("кадр %d отличается", i)
		}
	}

	// размеры по умолчанию из trex, данные с начала mdat
	same := [][]byte{bytes.Repeat([]byte{1}, 50), bytes.Repeat([]byte{2}, 50), bytes.Repeat([]byte{3}, 50)}
	var d Extractor
	es, err = d.Push(append(initSegment(7, "mp4a", 50), fragment(7, same, false)...))
	if err != nil {
		t.Fatal(err)
	}
	if got := adtsPayloads(t, es); len(got) != 3 || !bytes.Equal(got[2], same[2]) {
		t.Fatalf("кадры по умолчанию: %d", len(got))
	}

	// фрагмент чужой дорожки пропускается
	var o Extractor
	if es, err = o.Push(append(initSegment(1, "mp4a", 0), fragment(2, f1, true)...)); err != nil || len(es) != 0 {
		t.Fatalf("чужая дорожка: %d байт, %v", len(es), err)
	}

	for name, seg := range map[string][]byte{
		"шифрование":          append(initSegment(1, "enca", 0), fragment(1, f1, true)...),
		"не AAC":              append(initSegment(1, "Opus", 0), fragment(1, f1, true)...),
		"фрагмент без init":   append(mp4box("styp", []byte("msdh")), fragment(1, f1, true)...),
		"обрезанный фрагмент": append(initSegment(1, "mp4a", 0), fragment(1, f1, true)[:120]...),
	} {
		var x Extractor
		if _, err := x.Push(seg); err == nil {
			t.Errorf("%s: принято", name)
		} else if (name == "шифрование" || name == "не AAC") && !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: не ErrUnsupported: %v", name, err)
		}
	}
}

// Поток fMP4 по образцу SoundCloud: запись (VOD) с EXT-X-MAP; сегмент инициализации приходит один раз, вместе с первым сегментом.
func TestStreamFMP4(t *testing.T) {
	init := initSegment(1, "mp4a", 0)
	segs := [][]byte{fragment(1, aacFrames(3, 0x10), true), fragment(1, aacFrames(3, 0x20), true), fragment(1, aacFrames(2, 0x30), true)}
	inits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/aac_160k/playlist.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n"+
				"#EXT-X-MAP:URI=\"init.mp4?expires=1&Signature=x\"\n#EXTINF:10.0078,\ndata000.m4s?expires=1\n#EXTINF:10.0078,\ndata001.m4s?expires=1\n"+
				"#EXTINF:3.5,\ndata002.m4s?expires=1\n#EXT-X-ENDLIST\n")
		case r.URL.Path == "/aac_160k/init.mp4":
			inits++
			w.Write(init)
		case strings.HasPrefix(r.URL.Path, "/aac_160k/data00"):
			w.Write(segs[r.URL.Path[len("/aac_160k/data00")]-'0'])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := Open(context.Background(), srv.URL+"/aac_160k/playlist.m3u8", Options{})
	if err != nil {
		t.Fatal(err)
	}
	var e Extractor
	total := 0
	for i := 0; ; i++ {
		seg, err := s.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		es, err := e.Push(seg)
		if err != nil {
			t.Fatalf("сегмент %d: %v", i, err)
		}
		total += len(adtsPayloads(t, es))
	}
	if total != 8 || inits != 1 {
		t.Fatalf("кадров %d (ждали 8), сегмент инициализации скачан %d раз", total, inits)
	}
	if p, err := Parse("#EXTM3U\n#EXT-X-MAP:URI=\"i.mp4\",BYTERANGE=\"700@0\"\n#EXTINF:6,\na.m4s\n", mustURL("http://x/p.m3u8")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("EXT-X-MAP с BYTERANGE: %+v, %v", p, err)
	}
}
