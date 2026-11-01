package daemon

import (
	"errors"
	"reflect"
	"testing"

	"openmediad/internal/core/quasar/observe"
)

type replyStub struct {
	got [][]string
	ids []string
}

func (r *replyStub) OnReply(id string, d []string) {
	r.ids = append(r.ids, id)
	r.got = append(r.got, d)
}

// Ответ облака целиком уходит компонентам с replyHandler; сломанный компонент и компонент без интерфейса пропускаются.
func TestReplyIsDeliveredToHandlers(t *testing.T) {
	a, broken := &replyStub{}, &replyStub{}
	d := &daemon{comps: []*component{
		{name: "a", obj: a},
		{name: "none", obj: struct{}{}},
		{name: "broken", obj: broken, err: errors.New("не запустился")},
	}}
	d.reply("req-1", []string{"tts_play_placeholder", "audio_play"})
	if len(a.got) != 1 || a.ids[0] != "req-1" || !reflect.DeepEqual(a.got[0], []string{"tts_play_placeholder", "audio_play"}) {
		t.Fatalf("компонент получил %v %v", a.ids, a.got)
	}
	if len(broken.got) != 0 {
		t.Fatal("ответ ушёл компоненту, который не запустился")
	}
}

type trackStub struct{ got []string }

func (s *trackStub) OnReplyTrack(id, title, artist string) {
	s.got = append(s.got, id+"|"+title+"|"+artist)
}

func TestReplyTrackIsDeliveredToHandlers(t *testing.T) {
	a := &trackStub{}
	d := &daemon{comps: []*component{{name: "a", obj: a}, {name: "none", obj: struct{}{}}, {name: "broken", obj: &trackStub{}, err: errors.New("x")}}}
	d.replyTrack("req-1", observe.ReplyTrack{Title: "Loser", Subtitle: "Tame Impala"})
	if len(a.got) != 1 || a.got[0] != "req-1|Loser|Tame Impala" {
		t.Fatalf("компонент получил %v", a.got)
	}
}
