package explain

import (
	"encoding/json"
	"strings"
	"testing"

	"openmediad/internal/journal"
	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/pb"
	"openmediad/internal/quasar/wire"
)

// realLikeCommand — play с ключом и подписанным URL, как в трафике.
func realLikeCommand() msg.AudioCommand {
	md := pb.AppendString(nil, 1, "Hot N Cold")
	md = pb.AppendString(md, 2, "Katy Perry")
	d := pb.AppendString(nil, 1, "322454")
	d = pb.AppendString(d, 2, "https://strm-m9-2.strm.yandex.net/music-v2/crypt/ysign1=SECRETSIGNATURE,kts=1/0/1/aac192-mp4")
	d = pb.AppendString(d, 7, `{"callbacks":{"payload":{"@request_id":"SECRET-REQUEST-ID"}}}`)
	d = pb.AppendBytes(d, 9, md)
	d = pb.AppendString(d, 39, "8a9186c2262401e8226d78b000ff58b1")
	pl := pb.AppendUint(nil, 1, 1)
	pl = pb.AppendString(pl, 2, "2be3a8cf-df6a-4da5-b816-e89a568f978d")
	c := pb.AppendBytes(nil, 1, d)
	c = pb.AppendBytes(c, 22, pl)
	return pb.AppendString(c, 52, "AUDIO_PLAY directive")
}

func TestNoSecretsInEntries(t *testing.T) {
	frame := wire.Wrap(wire.TypeAudioCommand, realLikeCommand())
	e, ok := DescribeFrame(true, frame)
	if !ok {
		t.Fatal("кадр должен описываться")
	}
	b, _ := json.Marshal(e)
	for _, secret := range []string{"8a9186c2262401e8226d78b000ff58b1", "SECRETSIGNATURE", "SECRET-REQUEST-ID", "ysign1"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("в записи журнала просочилось %q: %s", secret, b)
		}
	}
	for _, want := range []string{"Hot N Cold", "Katy Perry", "strm-m9-2.strm.yandex.net", "322454"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("в записи нет %q: %s", want, b)
		}
	}
	if e.Why == "" {
		t.Fatal("у записи должно быть пояснение простыми словами")
	}
}

func TestExplainCoversVoiceAndEvents(t *testing.T) {
	voice := pb.AppendBytes(nil, 22, pb.AppendUint(pb.AppendString(nil, 2, "v1"), 1, 2))
	voice = pb.AppendString(voice, 29, "OggS....")
	e, _ := DescribeFrame(true, wire.Wrap(wire.TypeAudioCommand, voice))
	if !strings.Contains(e.Msg, "голос Алисы") || e.Lvl != journal.Trace {
		t.Fatalf("голосовой фрагмент: %q уровень %s", e.Msg, e.Lvl)
	}
	focus := pb.AppendUint(nil, 39, 1)
	e, _ = DescribeFrame(true, wire.Wrap(wire.TypeAudioCommand, focus))
	if !strings.Contains(e.Msg, "диалог занял") {
		t.Fatalf("focus 1: %q", e.Msg)
	}
	ev := pb.AppendUint(nil, 1, 1)
	ev = pb.AppendUint(ev, 2, 5)
	ev = pb.AppendBytes(ev, 4, pb.AppendString(pb.AppendUint(nil, 1, 1), 2, "abcdef0123456789"))
	e, _ = DescribeFrame(false, wire.Wrap(wire.TypeAudioEvent, ev))
	if !strings.Contains(e.Msg, "закончил") || e.Lvl != journal.Info {
		t.Fatalf("ENDED: %q уровень %s", e.Msg, e.Lvl)
	}
	prog := pb.AppendUint(nil, 1, 2)
	prog = pb.AppendUint(prog, 2, 3)
	e, _ = DescribeFrame(false, wire.Wrap(wire.TypeAudioEvent, prog))
	if e.Lvl != journal.Trace {
		t.Fatal("progress — фон")
	}
	if e, _ := DescribeFrame(true, []byte{1, 2, 3}); e.Lvl != journal.Warn {
		t.Fatal("кадр без конверта — предупреждение")
	}
}

func TestExplainFileStartTellsRingFromOneShot(t *testing.T) {
	for _, tc := range []struct {
		loop uint64
		want string
	}{{0, "звонок"}, {1, "системный звук"}} {
		d := pb.AppendString(pb.AppendUint(nil, 4, 4), 25, "/system/vendor/quasar/sounds/common/x.mp3")
		d = pb.AppendUint(d, 50, tc.loop)
		c := pb.AppendBytes(nil, 1, d)
		c = pb.AppendBytes(c, 22, pb.AppendString(pb.AppendUint(nil, 1, 4), 2, "f1e2d3c4-0000"))
		e, ok := DescribeFrame(true, wire.Wrap(wire.TypeAudioCommand, c))
		if !ok || !strings.Contains(e.Msg, tc.want) || e.Data["file_loops"] != (tc.loop == 0) {
			t.Errorf("50:%d: msg=%q data=%v", tc.loop, e.Msg, e.Data)
		}
	}
}

func TestNoteFileOnce(t *testing.T) {
	e := Note("file-once", "0123456789", "/system/vendor/quasar/sounds/common/spotter_start.mp3")
	if !strings.Contains(e.Msg, "spotter_start.mp3") || !strings.Contains(e.Msg, "играет mediad") || e.Why == "" {
		t.Fatalf("msg=%q why=%q", e.Msg, e.Why)
	}
}

func TestExplainFileStartShowsSystemSoundPath(t *testing.T) {
	d := pb.AppendUint(nil, 4, 4)
	d = pb.AppendString(d, 25, "/system/vendor/quasar/sounds/common/timer.wav")
	c := pb.AppendBytes(nil, 1, d)
	c = pb.AppendBytes(c, 22, pb.AppendString(pb.AppendUint(nil, 1, 4), 2, "f1e2d3c4-0000"))
	e, ok := DescribeFrame(true, wire.Wrap(wire.TypeAudioCommand, c))
	if !ok {
		t.Fatal("кадр должен описываться")
	}
	if !strings.Contains(e.Msg, "timer.wav") || e.Why == "" || e.Data["file"] != "/system/vendor/quasar/sounds/common/timer.wav" {
		t.Fatalf("запуск файла: msg=%q why=%q data=%v", e.Msg, e.Why, e.Data)
	}
}
