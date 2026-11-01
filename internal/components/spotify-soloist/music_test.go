package spotifysoloist

import (
	"strings"
	"testing"
	"time"
)

func TestIncludeMusicPhrases(t *testing.T) {
	for _, text := range []string{"Включи музыку", "Поставь музыку", "Алиса, включи музыку!", "Включи что-нибудь", "Музыку", "Включи мне музыку"} {
		if got := parsePhrase(text); got != actPlayMusic {
			t.Errorf("%q → %v", text, got)
		}
	}
	for _, text := range []string{"Включи музыку Кино", "Включи музыку для сна", "Включи яндекс музыку", "Включи радио", "Включи песню Кино", "Выключи музыку громко"} {
		if got := parsePhrase(text); got == actPlayMusic {
			t.Errorf("%q принято за «включи музыку»", text)
		}
	}
}

// Ничего не играет, Soloist помнит трек (пусть и устройство не выбрано в приложении): «включи музыку» продолжает его.
func TestIncludeMusicResumesLastSession(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	if !r.c.InterceptPhrase(reqID, "Включи музыку", false) {
		t.Fatal("«включи музыку» не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || strings.Contains(m, "uri") {
		t.Fatalf("продолжение должно быть play без uri: %s", m)
	}
	waitFor(t, "диалог Алисы отменён", func() bool { return r.cancels.Load() == 1 })
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса, если облако всё же пришлёт его, не отдан Spotify")
	}
}

func TestIncludeMusicStartsConfiguredContextWhenNothingToResume(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.MusicURI = "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" })
	// вход выполнен, но трека нет
	r.srv.push(t, evAuth)
	r.srv.push(t, `{"type":"playback_state","status":"idle"}`)
	waitFor(t, "состояние", func() bool { return r.c.ctl.state().LoggedIn && r.c.ctl.state().Status == "idle" })
	if !r.c.InterceptPhrase(reqID, "Включи музыку", false) {
		t.Fatal("фраза не перехвачена")
	}
	m := r.command(t)
	if !strings.Contains(m, `"command":"play"`) || !strings.Contains(m, `"uri":"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"`) {
		t.Fatalf("play с контекстом: %s", m)
	}
}

func TestIncludeMusicLeftToAliceWhenItCannotBeDone(t *testing.T) {
	cases := map[string]func(*testing.T) *voiceRig{
		"нечего продолжать и music_uri не задан": func(t *testing.T) *voiceRig {
			r := newVoiceRig(t, nil)
			r.srv.push(t, evAuth)
			r.srv.push(t, `{"type":"playback_state","status":"idle"}`)
			waitFor(t, "состояние", func() bool { return r.c.ctl.state().Status == "idle" })
			return r
		},
		"music = false": func(t *testing.T) *voiceRig {
			r := newVoiceRig(t, func(c *Config) { c.Music = false })
			r.state(t, "paused")
			return r
		},
		"Spotify уже играет": func(t *testing.T) *voiceRig {
			r := newVoiceRig(t, nil)
			r.state(t, "playing")
			return r
		},
		"играет Яндекс": func(t *testing.T) *voiceRig {
			r := newVoiceRig(t, nil)
			r.state(t, "paused")
			r.mach.playing.Store(true)
			return r
		},
		"не вошли в аккаунт": func(t *testing.T) *voiceRig { return newVoiceRig(t, nil) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			r := mk(t)
			if r.c.InterceptPhrase(reqID, "Включи музыку", false) {
				t.Fatal("фраза перехвачена, а делать нечего")
			}
			r.noCommand(t)
			time.Sleep(30 * time.Millisecond)
			if r.cancels.Load() != 0 {
				t.Fatal("диалог отменён зря: Алиса осталась бы без ответа")
			}
		})
	}
}

func TestMusicURIValidation(t *testing.T) {
	c := defaults()
	for _, ok := range []string{"", "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", "spotify:album:2msN7XBgV3JCjQ7Tq3t7i9", "spotify:artist:1Xyo4u8uXC1ZmMpatF05PJ"} {
		c.MusicURI = ok
		if err := c.validate(); err != nil {
			t.Errorf("%q отвергнут: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://example.com", "spotify:playlist:", "spotify:user:x:collection", "spotify:collection:tracks", "spotify:playlist:a b"} {
		c.MusicURI = bad
		if err := c.validate(); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
}
