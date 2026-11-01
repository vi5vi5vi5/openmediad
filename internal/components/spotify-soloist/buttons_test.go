package spotifysoloist

import (
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/quasar/buttons"
)

// Кнопка Алисы при играющем Spotify ставит его на паузу. Платформа своей сессии у Spotify не знает, поэтому сама ничего не сделает.
func TestToggleButtonPausesPlayingSpotify(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.OnButton(buttons.Toggle) {
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

// Spotify на паузе и сессия его: кнопка продолжает, а запуск очереди Яндекса, если платформа его затеет, достаётся Spotify.
func TestToggleButtonResumesPausedSpotify(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if !r.c.OnButton(buttons.Toggle) {
		t.Fatal("кнопка при Spotify на паузе не выполнена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("в Soloist ушло %s, ждали play", m)
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
}

// Играет Яндекс.Музыка — кнопка и жесты её, и Spotify их не трогает, хоть и стоит на паузе.
func TestButtonsLeaveYandexMusicAlone(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	r.mach.playing.Store(true)
	for _, b := range []buttons.Action{buttons.Toggle, buttons.Next, buttons.Prev} {
		if r.c.OnButton(b) {
			t.Fatalf("%s выполнено в Spotify, пока играет Яндекс", b)
		}
	}
	r.noCommand(t)
	if r.mach.armed.Load() != 0 {
		t.Fatal("запуск Яндекса отдан Spotify, хотя Яндекс играет по-настоящему")
	}
}

// Если нажатие платформа уже выполнила сама (сессия Spotify у неё усыновлена и пауза пришла по шине), из лога его не повторяем:
// иначе пауза тут же превратилась бы в продолжение.
func TestToggleButtonIsNotRepeatedAfterThePlatformDidIt(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.mach.platformCommand("pause")
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("команда платформы стала %s", m)
	}
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if r.c.OnButton(buttons.Toggle) {
		t.Fatal("то же нажатие выполнено второй раз")
	}
	r.noCommand(t)
	r.c.mu.Lock()
	r.c.platformAt = time.Now().Add(-2 * platformCmdEcho) // давно: следующее нажатие обычное
	r.c.mu.Unlock()
	if !r.c.OnButton(buttons.Toggle) {
		t.Fatal("нажатие через пару секунд после команды платформы потеряно")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("в Soloist ушло %s, ждали play", m)
	}
}

// Проведение по колонке листает Spotify, а запуск очереди Яндекса, который облако затеет в ответ на жест, достаётся Spotify.
func TestSwipeSkipsTracksAndArmsCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.OnButton(buttons.Next) {
		t.Fatal("жест вперёд не выполнен")
	}
	if m := r.command(t); !strings.Contains(m, `"skip_next"`) {
		t.Fatalf("жест вперёд стал %s", m)
	}
	if !r.c.OnButton(buttons.Prev) {
		t.Fatal("жест назад не выполнен")
	}
	if m := r.command(t); !strings.Contains(m, `"skip_prev"`) {
		t.Fatalf("жест назад стал %s", m)
	}
	if r.mach.armed.Load() != 2 {
		t.Fatalf("отдано запусков Яндекса: %d, ждали 2", r.mach.armed.Load())
	}
	// облако после жеста может прислать и директиву «следующий трек»: она повтор того же жеста
	r.c.OnDirective("player_previous_track")
	r.noCommand(t)
}

func TestButtonsCanBeSwitchedOff(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.Buttons = false })
	r.state(t, "playing")
	if r.c.OnButton(buttons.Toggle) || r.c.OnButton(buttons.Next) {
		t.Fatal("кнопки выключены, а Spotify среагировал")
	}
	r.noCommand(t)
}

func TestButtonsIgnoredWithoutLogin(t *testing.T) {
	r := newVoiceRig(t, nil) // событий входа не было: Soloist подключён, но не вошёл
	if r.c.OnButton(buttons.Toggle) {
		t.Fatal("кнопка выполнена без входа в Spotify")
	}
	r.noCommand(t)
}
