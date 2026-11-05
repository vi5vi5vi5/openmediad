package daemon

import (
	"testing"

	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
)

// Голосовые команды и директивы облака идут ядру, а ответ облака на перехваченную фразу голосом гасится (шлюз). Быстрая команда беззвучна
// и ничего не гасит.
func TestVoicePhraseGoesToThePlayerThroughTheHub(t *testing.T) {
	p := controltest.NewPlayer(controltest.Playing())
	d := newPlayerDaemon(&component{name: "spotify", obj: p})
	d.phrase("req1", "Алиса, пауза", false, false)
	d.phrase("req2", "Дальше", true, false)
	d.phrase("req3", "Сколько времени", false, false) // не про плеер
	got := p.Done()
	if len(got) != 2 || got[0] != (controltest.Done{Cmd: control.Pause, From: control.Voice}) || got[1] != (controltest.Done{Cmd: control.Next, From: control.FastVoice}) {
		t.Fatalf("плеер получил %v, ждали паузу голосом и «дальше» короткой командой", got)
	}
}

// Когда плеер команду не взял (играет Яндекс), фраза уходит Алисе как обычно.
func TestVoicePhraseStaysWithAliceWhenYandexPlays(t *testing.T) {
	p := controltest.NewPlayer(controltest.Playing())
	d := newPlayerDaemon(&component{name: "spotify", obj: p})
	yandexPlays(d)
	d.phrase("req1", "Пауза", false, false)
	if p.Count() != 0 {
		t.Fatalf("плеер получил %v, пока играет Яндекс", p.Done())
	}
}

// Директива облака — та же команда, но источник «облако»: ядро отсекает её, если фраза уже выполнена перехватом.
func TestCloudDirectiveGoesToTheHubAndEchoIsDropped(t *testing.T) {
	p := controltest.NewPlayer(controltest.Playing())
	d := newPlayerDaemon(&component{name: "spotify", obj: p})
	d.phrase("req1", "Дальше", false, false)
	d.directive("player_next_track") // эхо перехваченной фразы
	d.directive("draw_animation_directive")
	d.directive("player_pause")
	got := p.Done()
	if len(got) != 2 || got[0].Cmd != control.Next || got[1] != (controltest.Done{Cmd: control.Pause, From: control.Cloud}) {
		t.Fatalf("плеер получил %v", got)
	}
}

// Команда платформы виртуальному плееру (pause, release, play) идёт ядру источником «платформа».
func TestPlatformVirtualCommands(t *testing.T) {
	p := controltest.NewPlayer(controltest.Playing())
	d := newPlayerDaemon(&component{name: "spotify", obj: p})
	d.virtualCommand("pause")
	d.virtualCommand("release")
	d.virtualCommand("play")
	d.virtualCommand("other")
	got := p.Done()
	want := []controltest.Done{{Cmd: control.Pause, From: control.Virtual}, {Cmd: control.Pause, From: control.Virtual}, {Cmd: control.Resume, From: control.Virtual}}
	if len(got) != len(want) {
		t.Fatalf("плеер получил %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("команда %d: %v, ждали %v", i, got[i], want[i])
		}
	}
}
