package daemon

import (
	"errors"
	"testing"

	"openmediad/internal/core/control"
	"openmediad/internal/core/quasar/alicedclient"
)

type remoteRig struct {
	r       *platformRemote
	asked   []string
	sent    []alicedclient.Command
	playing bool
	fail    error
}

func newRemoteRig() *remoteRig {
	g := &remoteRig{}
	g.r = &platformRemote{
		playing: func() bool { return g.playing },
		ask:     func(s string) error { g.asked = append(g.asked, s); return g.fail },
		send:    func(c alicedclient.Command) error { g.sent = append(g.sent, c); return g.fail },
	}
	return g
}

// Пока музыка платформы играет, пауза идёт фразой (с анимацией на экране, как голосом); остальное — командами плееру, как кнопки колонки.
func TestPlatformRemoteWhilePlatformMusicPlays(t *testing.T) {
	g := newRemoteRig()
	g.playing = true
	for _, c := range []control.Cmd{control.Toggle, control.Pause, control.Resume, control.Next, control.Prev} {
		if err := g.r.Command(c); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	if len(g.asked) != 2 || g.asked[0] != "поставь на паузу" || g.asked[1] != "поставь на паузу" {
		t.Fatalf("пауза при играющей музыке — фразой: %v", g.asked)
	}
	if len(g.sent) != 2 || g.sent[0] != alicedclient.Next || g.sent[1] != alicedclient.Prev {
		t.Fatalf("команды плееру: %v (продолжать играющее не нужно)", g.sent)
	}
}

// Музыка платформы стоит: всё командами плееру, как кнопки колонки.
func TestPlatformRemoteWhilePlatformMusicIsSilent(t *testing.T) {
	g := newRemoteRig()
	for _, c := range []control.Cmd{control.Toggle, control.Pause, control.Resume, control.Next, control.Prev} {
		if err := g.r.Command(c); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	want := []alicedclient.Command{alicedclient.Toggle, alicedclient.Pause, alicedclient.Toggle, alicedclient.Next, alicedclient.Prev}
	if len(g.asked) != 0 || len(g.sent) != len(want) {
		t.Fatalf("фразы %v, команды %v", g.asked, g.sent)
	}
	for i := range want {
		if g.sent[i] != want[i] {
			t.Fatalf("команда %d: %v, ждали %v", i, g.sent[i], want[i])
		}
	}
}

func TestPlatformRemoteReportsFailureAndUnknownCommand(t *testing.T) {
	g := newRemoteRig()
	g.fail = errors.New("aliced не отвечает")
	if err := g.r.Command(control.Next); err == nil {
		t.Fatal("ошибку aliced надо вернуть")
	}
	g.fail = nil
	if err := g.r.Command(control.Cmd("jump")); err == nil {
		t.Fatal("незнакомую команду надо отвергнуть")
	}
}

// Демон даёт ядру пульт: команда извне при молчащих плеерах движка доходит до платформы одним вызовом Hub.Command.
func TestDaemonGivesHubThePlatformRemote(t *testing.T) {
	d := testDaemon(t)
	if d.hub.Remote == nil {
		t.Fatal("у ядра команд нет пульта платформы")
	}
	g := newRemoteRig()
	d.hub.Remote = g.r
	by, err := d.hub.Command(control.Next, control.API)
	if err != nil || by != control.OwnerPlatform || len(g.sent) != 1 || g.sent[0] != alicedclient.Next {
		t.Fatalf("выполнил %q, ошибка %v, платформе ушло %v", by, err, g.sent)
	}
}
