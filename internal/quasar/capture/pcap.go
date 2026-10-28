// Package capture — записи трафика шины: чтение tcpdump (classic pcap) с разбором TCP-потоков на кадры шины и
// обезличенный журнал кадров (.omdf) для золотых тестов в testdata/.
//
// Нужен тестам (сверка наших событий с настоящими) и инструментам разработчика (openmediad-dev). В демон не входит.
package capture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"openmediad/internal/quasar/pb"
	"openmediad/internal/quasar/wire"
)

// Frame — один кадр шины из записи.
type Frame struct {
	Time     time.Time
	Svc      string  // имя сервиса по порту (Ports)
	ToSvc    bool    // true — клиент → сервис, false — сервис → клиент
	Client   uint16  // порт клиентского сокета: различает соединения одного сервиса
	ClientIP [4]byte // адрес клиентской стороны: у соединений шима к настоящему сервису это 127.0.0.2
	Data     []byte  // кадр без длины; действителен только внутри вызова fn
}

// WalkPcap собирает кадры из записи tcpdump -i lo и отдаёт их fn по порядку записи. Сервис кадра определяется по Ports.
func WalkPcap(r io.Reader, fn func(Frame)) error { return WalkPcapPorts(r, Ports, fn) }

// WalkPcapPorts — WalkPcap со своей таблицей портов (например, с клиентской стороной шима 19906 как «audioclient»).
func WalkPcapPorts(r io.Reader, ports map[uint16]string, fn func(Frame)) error {
	flows := map[FlowKey]*Flow{}
	return ReadPcap(r, func(ts time.Time, link uint32, p []byte) {
		seg, ok := ParseTCP(link, p)
		if !ok {
			return
		}
		k := seg.Key
		var fr Frame
		if s, ok := ports[k.SrcPort]; ok {
			fr = Frame{Svc: s, Client: k.DstPort, ClientIP: k.Dst}
		} else if s, ok := ports[k.DstPort]; ok {
			fr = Frame{Svc: s, ToSvc: true, Client: k.SrcPort, ClientIP: k.Src}
		} else {
			return
		}
		f := flows[k]
		if f == nil {
			f = &Flow{}
			flows[k] = f
		}
		f.Add(seg.Seq, seg.Flags, seg.Payload, func(frame []byte) {
			fr.Time, fr.Data = ts, frame
			fn(fr)
		})
	})
}

// ReadPcap читает classic-pcap пакет за пакетом и вызывает fn; срез данных действителен только внутри вызова.
// Файл целиком в память не читается, поэтому подходит и для `tcpdump -w - | openmediad-dev pcap -`.
func ReadPcap(r io.Reader, fn func(ts time.Time, link uint32, p []byte)) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var hdr [24]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return fmt.Errorf("pcap: заголовок: %w", err)
	}
	var bo binary.ByteOrder
	var nano bool
	switch binary.LittleEndian.Uint32(hdr[:4]) {
	case 0xa1b2c3d4:
		bo = binary.LittleEndian
	case 0xa1b23c4d:
		bo, nano = binary.LittleEndian, true
	default:
		switch binary.BigEndian.Uint32(hdr[:4]) {
		case 0xa1b2c3d4:
			bo = binary.BigEndian
		case 0xa1b23c4d:
			bo, nano = binary.BigEndian, true
		default:
			return errors.New("pcap: неизвестный формат (нужен classic pcap, не pcapng)")
		}
	}
	link := bo.Uint32(hdr[20:24])

	var rec [16]byte
	var data []byte
	for {
		if _, err := io.ReadFull(br, rec[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		incl := bo.Uint32(rec[8:12])
		if incl > 1<<20 {
			return fmt.Errorf("pcap: пакет %d Б", incl)
		}
		if uint32(cap(data)) < incl {
			data = make([]byte, incl)
		}
		data = data[:incl]
		if _, err := io.ReadFull(br, data); err != nil {
			return nil
		}
		frac := int64(bo.Uint32(rec[4:8]))
		if !nano {
			frac *= 1000
		}
		fn(time.Unix(int64(bo.Uint32(rec[0:4])), frac).UTC(), link, data)
	}
}

// FlowKey — одно направление TCP-соединения.
type FlowKey struct {
	Src, Dst         [4]byte
	SrcPort, DstPort uint16
}

// Segment — один TCP-сегмент IPv4 из записи.
type Segment struct {
	Key     FlowKey
	Seq     uint32
	Flags   byte
	Payload []byte
}

// ParseTCP достаёт TCP-сегмент из пакета канального уровня; false — это не IPv4/TCP.
func ParseTCP(link uint32, p []byte) (Segment, bool) {
	var off int
	var proto uint16
	switch link {
	case 1: // Ethernet
		if len(p) < 14 {
			return Segment{}, false
		}
		proto, off = binary.BigEndian.Uint16(p[12:14]), 14
	case 113: // Linux cooked
		if len(p) < 16 {
			return Segment{}, false
		}
		proto, off = binary.BigEndian.Uint16(p[14:16]), 16
	case 276: // Linux cooked v2
		if len(p) < 20 {
			return Segment{}, false
		}
		proto, off = binary.BigEndian.Uint16(p[0:2]), 20
	default:
		return Segment{}, false
	}
	if proto != 0x0800 || len(p) < off+20 {
		return Segment{}, false
	}
	ip := p[off:]
	ihl := int(ip[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(ip[2:4]))
	if ip[9] != 6 || ihl < 20 || total > len(ip) || total < ihl+20 {
		return Segment{}, false
	}
	tcp := ip[ihl:total]
	doff := int(tcp[12]>>4) * 4
	if doff < 20 || doff > len(tcp) {
		return Segment{}, false
	}
	s := Segment{
		Seq:     binary.BigEndian.Uint32(tcp[4:8]),
		Flags:   tcp[13],
		Payload: tcp[doff:],
	}
	s.Key.SrcPort = binary.BigEndian.Uint16(tcp[0:2])
	s.Key.DstPort = binary.BigEndian.Uint16(tcp[2:4])
	copy(s.Key.Src[:], ip[12:16])
	copy(s.Key.Dst[:], ip[16:20])
	return s, true
}

// Flow собирает поток байт одного направления и режет его на кадры шины.
type Flow struct {
	next   uint32
	synced bool
	have   bool
	Buf    []byte // накопленный неразобранный хвост
}

// Add добавляет сегмент. Loopback идёт по порядку, поэтому достаточно отслеживать ожидаемый seq; при дыре поток
// пересинхронизируется по границе кадра. emit получает кадр без длины (действителен только внутри вызова).
func (f *Flow) Add(seq uint32, flags byte, payload []byte, emit func([]byte)) {
	const syn, rst, fin = 0x02, 0x04, 0x01
	if flags&syn != 0 {
		f.next, f.have, f.synced, f.Buf = seq+1, true, true, f.Buf[:0]
		return
	}
	if flags&(rst|fin) != 0 && len(payload) == 0 {
		f.have = false
		return
	}
	if len(payload) == 0 {
		return
	}
	if !f.have {
		f.next, f.have, f.synced = seq, true, false
	}
	if seq != f.next {
		delta := int32(seq - f.next)
		switch {
		case delta < 0 && int(-delta) < len(payload): // частичный повтор: берём только новый хвост
			payload = payload[-delta:]
			seq = f.next
		case delta < 0: // полный повтор
			return
		default: // потеря: начинаем заново
			f.Buf, f.synced = f.Buf[:0], false
		}
	}
	f.next = seq + uint32(len(payload))
	if !f.synced {
		if len(payload) < 4 {
			return
		}
		n := binary.LittleEndian.Uint32(payload[:4])
		if n == 0 || n > wire.MaxFrame {
			return
		}
		if int(n) <= len(payload)-4 && pb.Validate(payload[4:4+n]) != nil {
			return
		}
		f.synced = true
	}
	f.Buf = append(f.Buf, payload...)
	for len(f.Buf) >= 4 {
		n := binary.LittleEndian.Uint32(f.Buf[:4])
		if n == 0 || n > wire.MaxFrame {
			f.Buf, f.synced = f.Buf[:0], false
			return
		}
		if len(f.Buf) < 4+int(n) {
			return
		}
		emit(f.Buf[4 : 4+n])
		f.Buf = f.Buf[4+n:]
	}
	if cap(f.Buf) > 1<<20 && len(f.Buf) == 0 {
		f.Buf = nil
	}
}
