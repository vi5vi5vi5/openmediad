package spotifysoloist

import (
	"os"
	"strings"
	"testing"
)

// «Включи спотифай» при играющей Яндекс.Музыке: Spotify назван прямо, значит, запускаем его, а не даём Алисе искать плейлист с таким названием
// в Яндексе. Сессия продолжается без uri, диалог Алисы отменяется, запуск очереди Яндекса, если облако его затеет, достаётся Spotify.
func TestStartSpotifyByNameEvenWhileYandexPlays(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.mach.playing.Store(true)
	if !r.say("Включи спотифай", false) {
		t.Fatal("«включи спотифай» не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || strings.Contains(m, "uri") {
		t.Fatalf("продолжение должно быть play без uri: %s", m)
	}
	waitFor(t, "диалог Алисы отменён", func() bool { return r.cancels.Load() == 1 })
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
	if st := r.c.voiceStatus(); st.Handled != 1 || !strings.Contains(st.Last, "по названию") {
		t.Fatalf("статистика: %+v", st)
	}
}

// Распознаватель пишет Spotify по-английски; «спотик» и «музыку в спотике» понимаются так же.
func TestStartSpotifyWithEveryWayToNameIt(t *testing.T) {
	for _, text := range []string{"Включи Spotify", "Включи spotify.", "Включи спотик", "Запусти спотифай", "Включи музыку в спотифае", "Включи музыку на Spotify", "Включи что-нибудь из спотика"} {
		r := newVoiceRig(t, nil)
		r.state(t, "paused")
		if !r.say(text, false) {
			t.Errorf("%q не перехвачена", text)
			continue
		}
		if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
			t.Errorf("%q → %s", text, m)
		}
	}
}

// Названное прямо не подчиняется настройке music (она про «включи музыку» без названия), но подчиняется voice.
func TestStartSpotifyByNameIgnoresTheMusicSwitch(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.Music = false })
	r.state(t, "paused")
	if !r.say("Включи спотифай", false) {
		t.Fatal("music = false не должна мешать прямому «включи спотифай»")
	}
	r.command(t)
	off := newVoiceRig(t, func(c *Config) { c.Voice = false })
	off.state(t, "paused")
	if off.say("Включи спотифай", false) {
		t.Fatal("voice = false, а фраза перехвачена")
	}
	off.noCommand(t)
}

func TestStartSpotifyByNameFallsBackToMusicURI(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.MusicURI = "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" })
	r.srv.push(t, evAuth)
	r.srv.push(t, `{"type":"playback_state","status":"idle"}`)
	waitFor(t, "состояние", func() bool { return r.c.ctl.state().LoggedIn && r.c.ctl.state().Status == "idle" })
	if !r.say("Включи спотифай", false) {
		t.Fatal("фраза не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"uri":"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"`) {
		t.Fatalf("play с контекстом: %s", m)
	}
}

// Запускать нечего (нет прошлой сессии и music_uri) и быстрая команда платформы — фраза остаётся Алисе, диалог не отменяется.
func TestStartSpotifyByNameLeftToAliceWhenImpossible(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.srv.push(t, evAuth)
	r.srv.push(t, `{"type":"playback_state","status":"idle"}`)
	waitFor(t, "состояние", func() bool { return r.c.ctl.state().Status == "idle" })
	if r.say("Включи спотифай", false) {
		t.Fatal("перехват без прошлой сессии и music_uri")
	}
	r.noCommand(t)
	if r.cancels.Load() != 0 || r.mach.armed.Load() != 0 {
		t.Fatal("диалог отменён или очередь Яндекса отдана, хотя Spotify не запущен")
	}
	paused := newVoiceRig(t, nil)
	paused.state(t, "paused")
	if paused.say("Включи спотифай", true) {
		t.Fatal("быстрая команда платформы — это «пауза», «дальше», а не просьба с названием")
	}
	paused.noCommand(t)
}

// Свои слова из файла рядом с установкой: файл правят руками, и демон подхватывает правку без перезапуска.
func TestStartSpotifyFollowsThePhraseFile(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	path := r.c.p.phrases()
	if err := os.WriteFile(path, []byte("[names]\nспотичек\n[prepositions]\nв\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.say("Включи спотичек", false) {
		t.Fatal("новое название из файла не работает")
	}
	r.command(t)
	if r.say("Включи спотифай", false) {
		t.Fatal("«спотифай» убран из файла, а запускает Spotify")
	}
	r.noCommand(t)
	// опечатка в файле: прежние фразы остаются
	if err := os.WriteFile(path, []byte("[names]\nспотичек\nещё один\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.say("Включи спотичек", false) {
		t.Fatal("после опечатки в файле пропали прежние фразы")
	}
	r.command(t)
}

// Что говорит команда `openmediad spotify phrases test`: тот же разбор, что у голоса.
func TestClassifyPhrase(t *testing.T) {
	for text, want := range map[string]string{
		"Включи спотифай":         "Запустит Spotify",
		"Включи музыку в спотике": "Запустит Spotify",
		"Пауза": "Управление плеером: пауза",
		"Лайк":  "Фраза Spotify: лайк",
		"Включи Кино на спотифае": "Просьба по названию: ищу «кино»",
		"Включи свет":             "Не фраза Spotify",
		"Включи Кино":             "голая просьба",
		"Открой окно":             "Не фраза Spotify",
		"Включи музыку":           "Фраза Spotify: включить музыку",
	} {
		got := strings.Join(classifyPhrase(builtinBook, text), "\n")
		if !strings.Contains(got, want) {
			t.Errorf("%q → %q, ждали %q", text, got, want)
		}
	}
}
