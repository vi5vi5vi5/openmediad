package devtools

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openmediad/internal/quasar/capture"
	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/pb"
	"openmediad/internal/quasar/wire"
)

const (
	secretURL  = "https://strm-m9-2.strm.yandex.net/music-v2/crypt/ysign1=SECRETSIGNATURE0042,kts=1/0/1/aac192-mp4"
	secretKey  = "8a9186c2262401e8226d78b000ff58b1"
	secretReq  = "0f7b5b6e-1a2b-4c3d-9e8f-001122334455"
	secretJSON = `{"callbacks":{"payload":{"@request_id":"` + secretReq + `","url":"` + secretURL + `"}}}`
	title      = "Песня про секрет"
)

func playCmd() []byte {
	md := pb.AppendString(nil, 1, title)
	md = pb.AppendString(md, 2, "Исполнитель")
	d := pb.AppendString(nil, 1, "322454")
	d = pb.AppendString(d, 2, secretURL)
	d = pb.AppendString(d, 7, secretJSON)
	d = pb.AppendBytes(d, 8, pb.AppendString(pb.AppendString(nil, 1, secretReq), 2, "music"))
	d = pb.AppendBytes(d, 9, md)
	d = pb.AppendString(d, 39, secretKey)
	c := pb.AppendBytes(nil, 1, d)
	c = pb.AppendBytes(c, 22, pb.AppendString(pb.AppendUint(nil, 1, 1), 2, "2be3a8cf-df6a-4da5-b816-e89a568f978d"))
	return wire.Wrap(wire.TypeAudioCommand, pb.AppendString(c, 52, "AUDIO_PLAY directive"))
}

func eventWithURL() []byte {
	d := pb.AppendString(nil, 1, "322454")
	d = pb.AppendString(d, 2, secretURL+"?vsid=abc")
	d = pb.AppendBytes(d, 9, pb.AppendString(nil, 1, title))
	return wire.Wrap(wire.TypeAudioEvent, pb.AppendBytes(pb.AppendUint(nil, 1, 1), 3, d))
}

func voiceChunk() []byte {
	c := pb.AppendBytes(nil, 22, pb.AppendString(pb.AppendUint(nil, 1, 2), 2, "v1"))
	return wire.Wrap(wire.TypeAudioCommand, pb.AppendBytes(c, 29, []byte("OggS-голос-с-личными-данными")))
}

func writeInput(t *testing.T, frames ...capture.Frame) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.omdf")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := capture.NewLogWriter(f)
	for _, fr := range frames {
		if err := w.Write(fr); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestSanitizeRemovesSecretsKeepsStructure(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	in := writeInput(t,
		capture.Frame{Time: t0, Svc: "audioclient", ToSvc: true, Client: 40000, ClientIP: [4]byte{127, 0, 0, 1}, Data: playCmd()},
		capture.Frame{Time: t0.Add(time.Millisecond), Svc: "audioclient", Client: 40000, ClientIP: [4]byte{127, 0, 0, 1}, Data: eventWithURL()},
		capture.Frame{Time: t0.Add(2 * time.Millisecond), Svc: "aliced", Client: 40001, Data: wire.Wrap(wire.TypeAliceState, pb.AppendString(nil, 2, "личная фраза"))},
		capture.Frame{Time: t0.Add(3 * time.Millisecond), Svc: "audioclient", ToSvc: true, Client: 40000, Data: voiceChunk()},
	)
	var out bytes.Buffer
	st, err := Sanitize(in, &out, SanitizeOpt{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Frames != 3 || st.Dropped != 1 {
		t.Fatalf("итог: %s (ждали 3 кадра и 1 отброшенный — aliced)", st)
	}
	raw := out.String()
	for _, s := range []string{"SECRETSIGNATURE0042", secretKey, secretReq, "ysign1=SECRET", title, "Исполнитель", "личн"} {
		if strings.Contains(raw, s) {
			t.Fatalf("в обезличенной записи осталось %q", s)
		}
	}
	var got []capture.Frame
	err = capture.WalkLog(bytes.NewReader(out.Bytes()), func(f capture.Frame) {
		f.Data = append([]byte(nil), f.Data...)
		got = append(got, f)
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("чтение обратно: %v, кадров %d", err, len(got))
	}
	if !got[0].Time.Equal(t0) || got[0].Client != 40000 || !got[0].ToSvc || got[0].ClientIP != [4]byte{127, 0, 0, 1} {
		t.Fatalf("служебные поля кадра не сохранились: %+v", got[0])
	}
	// команда по-прежнему разбирается, длины полей те же, URL события = URL команды + ?vsid=…
	m, err := wire.Unwrap(got[0].Data)
	if err != nil || len(got[0].Data) != len(playCmd()) {
		t.Fatalf("кадр команды испорчен: %v", err)
	}
	cmd := msg.AudioCommand(m.Body)
	tr, ok := cmd.Track()
	if !ok || cmd.Classify() != msg.CmdPlay || len(tr.URL) != len(secretURL) || !strings.HasPrefix(tr.URL, "https://") {
		t.Fatalf("команда после обезличивания: %+v %v", tr, cmd.Classify())
	}
	em, _ := wire.Unwrap(got[1].Data)
	d, _ := pb.Bytes(em.Body, 3)
	evURL, _ := pb.Str(d, 2)
	if evURL != tr.URL+"?vsid=abc" {
		t.Fatalf("связь URL события и команды потеряна:\n%s\n%s", evURL, tr.URL)
	}
	vm, _ := wire.Unwrap(got[2].Data)
	chunk, _ := msg.AudioCommand(vm.Body).VoiceChunk()
	if len(chunk) == 0 || bytes.Count(chunk, []byte{0}) != len(chunk) {
		t.Fatalf("голос должен стать тишиной той же длины: %q", chunk)
	}
}

func TestSanitizeKeepOptions(t *testing.T) {
	in := writeInput(t,
		capture.Frame{Svc: "audioclient", ToSvc: true, Data: playCmd()},
		capture.Frame{Svc: "audioclient", ToSvc: true, Data: voiceChunk()},
	)
	var out bytes.Buffer
	if _, err := Sanitize(in, &out, SanitizeOpt{KeepTitles: true, KeepVoice: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), title) || !strings.Contains(out.String(), "OggS-голос") {
		t.Fatal("с -keep-titles и -keep-voice названия и голос должны остаться")
	}
	if strings.Contains(out.String(), secretKey) {
		t.Fatal("ключ потока заменяется всегда")
	}
}
