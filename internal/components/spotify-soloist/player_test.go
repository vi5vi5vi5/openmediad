package spotifysoloist

import (
	"strings"
	"testing"
	"time"

	playctl "openmediad/internal/core/control"
)

// Spotify — плеер ядра: интерфейс выполнен (на этом держится подключение в демоне).
var _ playctl.Player = (*Component)(nil)

// Кнопка Алисы при играющем Spotify ставит его на паузу. Платформа своей сессии у Spotify не знает, поэтому сама ничего не сделает.
func TestToggleFromTheSpeakerPausesPlayingSpotify(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.Handle(playctl.Toggle, playctl.Speaker) {
		t.Fatal("кнопка при играющем Spotify не выполнена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("в Soloist ушло %s, ждали pause", m)
	}
	if r.mach.armed.Load() != 0 {
		t.Fatal("после паузы платформа ничего не запускает, а запуск отдан Spotify")
	}
	if st := r.c.voiceStatus(); st.Handled != 1 || !strings.Contains(st.Last, "кнопка") {
		t.Fatalf("статистика: %+v", st)
	}
}

// Spotify на паузе и сессия его: кнопка колонки продолжает, а запуск очереди Яндекса, если платформа его затеет, достаётся Spotify.
func TestToggleFromTheSpeakerResumesPausedSpotifyAndArmsCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if !r.c.Handle(playctl.Toggle, playctl.Speaker) {
		t.Fatal("кнопка при Spotify на паузе не выполнена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("в Soloist ушло %s, ждали play", m)
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
}

// Кнопки наушников платформа не слышит: она ничего не запросит у облака, и очередь Яндекса для них не готовят,
// иначе первый настоящий запуск Яндекса в ближайшие секунды был бы потерян.
func TestHeadphonesPauseResumeAndSkipDoNotArmTheCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	for _, step := range []struct {
		cmd  playctl.Cmd
		want string
	}{{playctl.Toggle, `"command":"pause"`}, {playctl.Next, `"skip_next"`}, {playctl.Prev, `"skip_prev"`}} {
		if !r.c.Handle(step.cmd, playctl.Headphones) {
			t.Fatalf("%s с наушников не выполнено", step.cmd)
		}
		if m := r.command(t); !strings.Contains(m, step.want) {
			t.Fatalf("%s стало %s, ждали %s", step.cmd, m, step.want)
		}
	}
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if !r.c.Handle(playctl.Toggle, playctl.Headphones) {
		t.Fatal("продолжить с наушников не выполнено")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("в Soloist ушло %s, ждали play", m)
	}
	if r.mach.armed.Load() != 0 {
		t.Fatalf("очередь Яндекса подготовлена %d раз для нажатий, о которых платформа не знает", r.mach.armed.Load())
	}
}

// Защита от повтора нужна только кнопке колонки: её видят и платформа, и лог. Нажатие в наушниках — отдельное событие, и оно сработает,
// даже если платформа только что управляла плеером.
func TestPlatformEchoGuardOnlyForTheSpeaker(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.mach.platformCommand("pause")
	r.command(t)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if r.c.Handle(playctl.Toggle, playctl.Speaker) {
		t.Fatal("то же нажатие на колонке выполнено второй раз")
	}
	r.noCommand(t)
	if !r.c.Handle(playctl.Toggle, playctl.Headphones) {
		t.Fatal("нажатие в наушниках проглочено защитой от повтора колонки")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("в Soloist ушло %s, ждали play", m)
	}
	// и через пару секунд кнопка колонки снова обычная
	time.Sleep(10 * time.Millisecond)
	r.state(t, "playing")
	r.c.mu.Lock()
	r.c.platformAt = time.Now().Add(-2 * platformCmdEcho)
	r.c.mu.Unlock()
	if !r.c.Handle(playctl.Toggle, playctl.Speaker) {
		t.Fatal("нажатие через пару секунд после команды платформы потеряно")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("в Soloist ушло %s, ждали pause", m)
	}
}

// Явные «пауза» и «продолжить» (не переключатель) выполняются по тем же правилам хозяина звука.
func TestExplicitPauseAndResume(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if r.c.Handle(playctl.Resume, playctl.Headphones) {
		t.Fatal("«продолжить» при играющем Spotify выполнено")
	}
	r.noCommand(t)
	if !r.c.Handle(playctl.Pause, playctl.Headphones) {
		t.Fatal("«пауза» при играющем Spotify не выполнена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("в Soloist ушло %s", m)
	}
}

// Проведение по колонке листает Spotify, а запуск очереди Яндекса, который облако затеет в ответ на жест, достаётся Spotify.
func TestSwipeSkipsTracksAndArmsCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.Handle(playctl.Next, playctl.Speaker) {
		t.Fatal("жест вперёд не выполнен")
	}
	if m := r.command(t); !strings.Contains(m, `"skip_next"`) {
		t.Fatalf("жест вперёд стал %s", m)
	}
	if !r.c.Handle(playctl.Prev, playctl.Speaker) {
		t.Fatal("жест назад не выполнен")
	}
	if m := r.command(t); !strings.Contains(m, `"skip_prev"`) {
		t.Fatalf("жест назад стал %s", m)
	}
	if r.mach.armed.Load() != 2 {
		t.Fatalf("отдано запусков Яндекса: %d, ждали 2", r.mach.armed.Load())
	}
	// облако после жеста может прислать и директиву «предыдущий трек»: она повтор того же жеста
	r.c.OnDirective("player_previous_track")
	r.noCommand(t)
}

// Играет Яндекс.Музыка — команды её, и Spotify их не трогает, хоть и стоит на паузе.
func TestSpotifyLeavesYandexMusicAlone(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	r.mach.playing.Store(true)
	for _, from := range []playctl.Origin{playctl.Speaker, playctl.Headphones} {
		for _, cmd := range []playctl.Cmd{playctl.Toggle, playctl.Pause, playctl.Resume, playctl.Next, playctl.Prev} {
			if r.c.Handle(cmd, from) {
				t.Fatalf("%s (%s) выполнено в Spotify, пока играет Яндекс", cmd, from)
			}
		}
	}
	r.noCommand(t)
	if r.mach.armed.Load() != 0 {
		t.Fatal("запуск Яндекса отдан Spotify, хотя Яндекс играет по-настоящему")
	}
}

func TestPlayerCommandsCanBeSwitchedOff(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.Buttons = false })
	r.state(t, "playing")
	if r.c.Handle(playctl.Toggle, playctl.Headphones) || r.c.Handle(playctl.Next, playctl.Speaker) {
		t.Fatal("кнопки выключены, а Spotify среагировал")
	}
	r.noCommand(t)
}

func TestPlayerIgnoresCommandsWithoutLogin(t *testing.T) {
	r := newVoiceRig(t, nil) // событий входа не было: Soloist подключён, но не вошёл
	if r.c.Handle(playctl.Toggle, playctl.Headphones) {
		t.Fatal("команда выполнена без входа в Spotify")
	}
	r.noCommand(t)
}
