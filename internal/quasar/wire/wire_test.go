package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"openmediad/internal/quasar/pb"
)

// Эталонные кадры сняты с живой колонки командой maind --cli (клавиши 'c' и 'o'), прошивка .117.
const (
	realCancelDialog = "e20f153a0f416c6963654361706162696c69747942020803e80fd201"
	realPause        = "e20f593a19506c61796261636b436f6e74726f6c4361706162696c6974796a3c08012a36080e10031801222434613039363130312d303131342d343665622d623933352d64323762653065623362613132084b6579626f6172643800e80fd201"
	realPauseID      = "4a096101-0114-46eb-b935-d27be0eb3ba1"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCancelDialogMatchesRealTraffic(t *testing.T) {
	if got, want := CancelDialog(), unhex(t, realCancelDialog); !bytes.Equal(got, want) {
		t.Fatalf("CancelDialog:\n получили %x\n ждали    %x", got, want)
	}
}

func TestPausePlaybackMatchesRealTraffic(t *testing.T) {
	if got, want := PausePlayback(realPauseID, "Keyboard"), unhex(t, realPause); !bytes.Equal(got, want) {
		t.Fatalf("PausePlayback:\n получили %x\n ждали    %x", got, want)
	}
}

// Текстовый запрос «поставь на паузу» — как в записи mdbg1.pcap (maind --cli, 'q').
func TestTextInputMatchesRealTraffic(t *testing.T) {
	want := unhex(t, "e20fb4023a0f416c6963654361706162696c69747942a00208001299020a2463666662383733382d356338302d343233362d383965662d6361306334306265346535381299017b226173725f726573756c74223a5b5d2c2261747461636865645f66696c6573223a5b5d2c2274657874223a225c75303433665c75303433655c75303434315c75303434325c75303433305c75303433325c7530343463205c75303433645c7530343330205c75303433665c75303433305c75303434335c75303433375c7530343433222c2274797065223a22746578745f696e707574227d18002001280030003a2c080210031801222430313065633137372d616166662d346565632d396363612d383939363039386632383632400048005000580060017a008201008a0100900101980100a00101aa0100ca01001800e80fd201")
	got := TextInput("cffb8738-5c80-4236-89ef-ca0c40be4e58", "010ec177-aaff-4eec-9cca-8996098f2862", "поставь на паузу")
	if !bytes.Equal(got, want) {
		t.Fatalf("TextInput:\n получили %x\n ждали    %x", got, want)
	}
	bs := `\`
	wantJS := `"a` + bs + `"` + bs + bs + bs + "ud83d" + bs + "ude00" + `"`
	if s := jsonASCII("a\"\\" + string(rune(0x1F600))); s != wantJS {
		t.Fatalf("экранирование: %s, ждали %s", s, wantJS)
	}
}

// Кадры maind --cli ('p', 'n', 'l' и регистрация клиента), запись 28.10.2026.
func TestPlaybackCommandsMatchRealTraffic(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []byte
		want string
	}{
		{"TogglePlayback", TogglePlayback("c515cff6-e892-4684-95fd-2bf9fe14000c", "Keyboard"),
			"e20f5b3a19506c61796261636b436f6e74726f6c4361706162696c6974796a3e080210012a36080e10031801222463353135636666362d653839322d343638342d393566642d32626639666531343030306332084b6579626f6172643800e80fd201"},
		{"NextTrack", NextTrack("894155f0-c9ab-4132-9165-a56460e62234", "Keyboard"),
			"e20f593a19506c61796261636b436f6e74726f6c4361706162696c6974796a3c08062a36080e10031801222438393431353566302d633961622d343133322d393136352d61353634363065363232333432084b6579626f6172643800e80fd201"},
		{"PrevTrack", PrevTrack("70f7c518-7649-4c46-8bec-84ca617bec6f", "Keyboard"),
			"e20f593a19506c61796261636b436f6e74726f6c4361706162696c6974796a3c08072a36080e10031801222437306637633531382d373634392d346334362d386265632d38346361363137626563366632084b6579626f6172643800e80fd201"},
		{"RegisterEndpoint", RegisterEndpoint("75a8a194-aca7-47aa-8816-15d0f422db21", "cli"),
			"e20f410a2d0a2437356138613139342d616361372d343761612d383831362d31356430663432326462323110022203636c693a10456e64706f696e7453746f726167655fe80fd201"},
	} {
		if want := unhex(t, c.want); !bytes.Equal(c.got, want) {
			t.Errorf("%s:\n получили %x\n ждали    %x", c.name, c.got, want)
		}
	}
}

func TestUnwrapRealFrames(t *testing.T) {
	m, err := Unwrap(unhex(t, realCancelDialog))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeRemoting {
		t.Fatalf("тип %v", m.Type)
	}
	if obj, _ := pb.Str(m.Body, 7); obj != "AliceCapability" {
		t.Fatalf("объект %q", obj)
	}
	method, _ := pb.Bytes(m.Body, 8)
	if kind, _ := pb.Uint(method, 1); kind != 3 {
		t.Fatalf("вид метода %d, ждали 3 (CANCEL_DIALOG)", kind)
	}

	m, err = Unwrap(unhex(t, realPause))
	if err != nil {
		t.Fatal(err)
	}
	method, _ = pb.Bytes(m.Body, 13)
	src, _ := pb.Bytes(method, 5)
	if id, _ := pb.Str(src, 4); id != realPauseID {
		t.Fatalf("uuid %q", id)
	}
	if s, _ := pb.Str(src, 6); s != "Keyboard" {
		t.Fatalf("source %q", s)
	}
}

func TestWrapUnwrap(t *testing.T) {
	body := pb.AppendString(nil, 1, "x")
	m, err := Unwrap(Wrap(TypeAliceState, body))
	if err != nil || m.Type != TypeAliceState || !bytes.Equal(m.Body, body) {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestUnwrapExtraFieldsAreKept(t *testing.T) {
	// metricad кладёт рядом с конвертом ещё поле 1 (UUID события).
	frame := append(Wrap(TypeMetrica, []byte{0x0a, 0x00}), pb.AppendString(nil, 1, "ev-1")...)
	m, err := Unwrap(frame)
	if err != nil || m.Type != TypeMetrica {
		t.Fatalf("%+v %v", m, err)
	}
	if s, _ := pb.Str(m.Raw, 1); s != "ev-1" {
		t.Fatalf("дополнительное поле потеряно: %q", s)
	}
}

func TestUnwrapErrors(t *testing.T) {
	if _, err := Unwrap(pb.AppendString(nil, 252, "тело")); !errors.Is(err, ErrNoEnvelope) {
		t.Errorf("нет типа: %v", err)
	}
	if _, err := Unwrap(pb.AppendUint(nil, 253, 3)); !errors.Is(err, ErrNoEnvelope) {
		t.Errorf("нет тела: %v", err)
	}
	if _, err := Unwrap([]byte{0x0a, 0x05, 'a'}); !errors.Is(err, pb.ErrTruncated) {
		t.Errorf("битый кадр: %v", err)
	}
}

func TestFrameReader(t *testing.T) {
	a, b := CancelDialog(), PausePlayback(realPauseID, "x")
	stream := AppendFrame(AppendFrame(nil, a), b)
	fr := NewFrameReader(bytes.NewReader(stream))
	for i, want := range [][]byte{a, b} {
		got, err := fr.Next()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("кадр %d: %v", i, err)
		}
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("ждали io.EOF, получили %v", err)
	}
}

func TestFrameReaderRejectsBadLengths(t *testing.T) {
	var h [4]byte
	for name, n := range map[string]uint32{"нулевая": 0, "больше лимита": MaxFrame + 1} {
		binary.LittleEndian.PutUint32(h[:], n)
		if _, err := NewFrameReader(bytes.NewReader(h[:])).Next(); !errors.Is(err, ErrFrameSize) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// обрыв посреди кадра
	binary.LittleEndian.PutUint32(h[:], 10)
	if _, err := NewFrameReader(bytes.NewReader(append(h[:], 1, 2, 3))).Next(); err != io.ErrUnexpectedEOF {
		t.Errorf("обрыв: %v", err)
	}
}

func TestFrameReaderDropsBigBuffer(t *testing.T) {
	big := bytes.Repeat([]byte{0x20}, keepBuf*2) // 0x20 — не валидный protobuf, но FrameReader это не проверяет
	fr := NewFrameReader(bytes.NewReader(AppendFrame(AppendFrame(nil, big), []byte{1})))
	if _, err := fr.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := fr.Next(); err != nil {
		t.Fatal(err)
	}
	if cap(fr.buf) > keepBuf {
		t.Errorf("крупный буфер должен отпускаться, cap=%d", cap(fr.buf))
	}
}

func TestTypeString(t *testing.T) {
	if TypeAudioEvent.String() != "AudioEvent" || Type(999).String() != "type999" {
		t.Error("Type.String")
	}
}
