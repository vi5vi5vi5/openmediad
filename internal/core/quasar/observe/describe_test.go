package observe

import (
	"strings"
	"testing"

	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
)

func TestDescribe(t *testing.T) {
	voice := pb.AppendBytes(nil, 22, pb.AppendUint(pb.AppendString(pb.AppendUint(nil, 1, 2), 2, "72daa5dc-1111"), 4, 0))
	chunk := wire.Wrap(wire.TypeAudioCommand, append(voice, pb.AppendBytes(nil, 29, make([]byte, 8137))...))
	m, err := wire.Unwrap(chunk)
	if err != nil {
		t.Fatal(err)
	}
	line, show := Describe("audioclient", m, Opt{})
	if !show || !strings.Contains(line, "voice-chunk") || !strings.Contains(line, "8137B") || !strings.Contains(line, "voice/72daa5dc") {
		t.Errorf("%q show=%v", line, show)
	}

	// прогресс плеера по умолчанию скрыт, с -progress виден
	ev := wire.Wrap(wire.TypeAudioEvent, append(pb.AppendUint(nil, 1, 2), pb.AppendUint(nil, 2, 3)...))
	m, _ = wire.Unwrap(ev)
	if _, show := Describe("audioclient", m, Opt{}); show {
		t.Error("прогресс должен быть скрыт")
	}
	if line, show := Describe("audioclient", m, Opt{Progress: true}); !show || !strings.Contains(line, "progress PLAYING") {
		t.Errorf("%q %v", line, show)
	}

	// фон: IOHub скрыт без -noisy
	m, _ = wire.Unwrap(wire.Wrap(wire.TypeIOHub, pb.AppendUint(nil, 1, 1)))
	if _, show := Describe("iohub_clients", m, Opt{}); show {
		t.Error("iohub должен быть скрыт")
	}
	if line, show := Describe("iohub_clients", m, Opt{Noisy: true}); !show || !strings.HasPrefix(line, "IOHub") {
		t.Errorf("%q %v", line, show)
	}
}

func TestDescribeHidesURLTokens(t *testing.T) {
	secret := "ysign1=deadbeefdeadbeef"
	url := "https://strm-m9-75.strm.yandex.net/music-v2/crypt/" + secret + "/x"
	track := pb.AppendString(nil, 1, "44683635")
	track = pb.AppendString(track, 2, url)
	body := pb.AppendBytes(nil, 1, track)
	body = pb.AppendBytes(body, 22, pb.AppendString(pb.AppendUint(nil, 1, 1), 2, "76006c91-x"))
	m, _ := wire.Unwrap(wire.Wrap(wire.TypeAudioCommand, body))
	line, _ := Describe("audioclient", m, Opt{})
	if strings.Contains(line, "ysign") || !strings.Contains(line, "strm-m9-75.strm.yandex.net") {
		t.Errorf("в строке должен остаться только хост: %q", line)
	}
}

func TestDescribeAliceReply(t *testing.T) {
	reply := pb.AppendString(nil, 1, "Включаю.")
	reply = pb.AppendBytes(reply, 5, []byte(`{"response":{"directives":[{"name":"tts_play_placeholder"},{"name":"audio_play"}]}}`))
	s := pb.AppendUint(nil, 1, 3)
	s = pb.AppendString(s, 2, "Включи музыку")
	s = pb.AppendBytes(s, 3, reply)
	m, _ := wire.Unwrap(wire.Wrap(wire.TypeAliceState, s))
	line, _ := Describe("aliced", m, Opt{})
	for _, want := range []string{"BUSY", `text="Включи музыку"`, `reply="Включаю."`, "[tts_play_placeholder audio_play]"} {
		if !strings.Contains(line, want) {
			t.Errorf("нет %q в %q", want, line)
		}
	}
}
