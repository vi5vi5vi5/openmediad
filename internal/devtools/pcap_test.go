package devtools

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"openmediad/internal/quasar/pb"
	"openmediad/internal/quasar/wire"
)

// pcapPacket собирает одну запись classic-pcap: Ethernet + IPv4 + TCP с payload.
func pcapPacket(sport, dport uint16, seq uint32, flags byte, payload []byte) []byte {
	pkt := make([]byte, 14+20+20, 14+20+20+len(payload))
	binary.BigEndian.PutUint16(pkt[12:], 0x0800)
	ip := pkt[14:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+20+len(payload)))
	ip[9] = 6
	copy(ip[12:], []byte{127, 0, 0, 1})
	copy(ip[16:], []byte{127, 0, 0, 1})
	tcp := ip[20:]
	binary.BigEndian.PutUint16(tcp[0:], sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], seq)
	tcp[12] = 5 << 4
	tcp[13] = flags
	pkt = append(pkt, payload...)

	rec := make([]byte, 16)
	binary.LittleEndian.PutUint32(rec[0:], 1793049083)
	binary.LittleEndian.PutUint32(rec[4:], 123000)
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(pkt)))
	return append(rec, pkt...)
}

func TestDecodePcapSynthetic(t *testing.T) {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:], 2)
	binary.LittleEndian.PutUint16(hdr[6:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 65535)
	binary.LittleEndian.PutUint32(hdr[20:], 1)

	state := pb.AppendUint(nil, 1, 1)
	state = pb.AppendString(state, 2, "Пауза")
	frame := wire.AppendFrame(nil, wire.Wrap(wire.TypeAliceState, state))
	cancel := wire.AppendFrame(nil, wire.CancelDialog())

	cap := append([]byte(nil), hdr...)
	cap = append(cap, pcapPacket(9875, 40000, 100, 0x02, nil)...) // SYN сервера
	cap = append(cap, pcapPacket(9875, 40000, 101, 0x18, frame)...)
	cap = append(cap, pcapPacket(40000, 9875, 7, 0x18, cancel)...) // клиент → сервер, без SYN
	cap = append(cap, pcapPacket(40001, 40002, 1, 0x18, frame)...) // не наш порт — игнорируется

	var out bytes.Buffer
	if err := DecodePcap(bytes.NewReader(cap), PcapOpt{}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "S→C AliceState LISTENING") || !strings.Contains(s, `text="Пауза"`) {
		t.Errorf("нет ответа сервера:\n%s", s)
	}
	if !strings.Contains(s, "C→S Remoting AliceCapability methods=[8]") {
		t.Errorf("нет команды клиента:\n%s", s)
	}
	if n := strings.Count(s, "\n"); n != 2 {
		t.Errorf("ждали 2 строки, получили %d:\n%s", n, s)
	}
}

func TestDecodePcapRejectsPcapng(t *testing.T) {
	if err := DecodePcap(bytes.NewReader(append([]byte{0x0a, 0x0d, 0x0d, 0x0a}, make([]byte, 40)...)), PcapOpt{}, &bytes.Buffer{}); err == nil {
		t.Error("pcapng должен отвергаться понятной ошибкой")
	}
}

// Реплей настоящей записи шины: OMD_PCAP=путь\к\music.pcap go test ./internal/devtools -run Real -v
// Запись содержит идентификаторы аккаунта, поэтому в репозитории её нет — тест пропускается без переменной.
func TestRealCapture(t *testing.T) {
	path := os.Getenv("OMD_PCAP")
	if path == "" {
		t.Skip("OMD_PCAP не задан")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if err := DecodePcap(f, PcapOpt{Progress: true}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"AliceState LISTENING", "final", "directives=[tts_play_placeholder audio_play",
		"AudioCommand voice-start", "AudioCommand voice-chunk", "AudioCommand voice-eos", "AudioCommand release",
		"AudioCommand play", "AudioCommand prefetch", "AudioCommand pause",
		"AudioEvent progress PLAYING", "AudioEvent state ENDED", "AudioEvent state PAUSED",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("в реальной записи не найдено %q", want)
		}
	}
	t.Logf("строк в разборе: %d", strings.Count(s, "\n"))
}
