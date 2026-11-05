package spotifysoloist

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// likeRig — компонент с поддельным Spotify и выполненным входом в Web API.
func likeRig(t *testing.T, edit func(*Config)) (*voiceRig, *fakeSpotify) {
	t.Helper()
	f := newFakeSpotify(t)
	r := newVoiceRig(t, func(c *Config) {
		c.ClientID = testClientID
		if edit != nil {
			edit(c)
		}
	})
	w := newWebAPI(newPaths(t.TempDir(), t.TempDir(), ""), func() string { return testClientID })
	w.auth, w.api = f.URL, f.URL+"/v1"
	login(t, w, f)
	r.c.web = w
	return r, f
}

func (f *fakeSpotify) waitCalls(t *testing.T, n int) []string {
	t.Helper()
	var calls []string
	waitFor(t, "обращения к Web API", func() bool { calls = f.callLog(); return len(calls) >= n })
	return calls
}

func TestLikePhrasesAndLookalikes(t *testing.T) {
	for text, want := range map[string]action{
		"Лайк": actLike, "Поставь лайк": actLike, "лайкни": actLike, "Мне нравится": actLike, "Мне нравится эта песня!": actLike,
		"Алиса, поставь лайк": actLike, "Добавь в избранное": actLike,
		"Убери лайк": actUnlike, "Сними лайк": actUnlike, "Мне больше не нравится": actUnlike, "Убери из избранного": actUnlike,
	} {
		if got := parsePhrase(text); got != want {
			t.Errorf("%q → %v, ждали %v", text, got, want)
		}
	}
	for _, text := range []string{"Дизлайк", "Поставь лайк фотографии", "Мне нравится погода", "Лайкни машину", "Что мне нравится", "Нравится ли тебе музыка",
		"Поставь лайк в инстаграме", "Добавь в избранное фильм"} {
		if got := parsePhrase(text); got != actNone {
			t.Errorf("%q принято за %v", text, got)
		}
	}
}

func TestLikeWhileSpotifyPlaying(t *testing.T) {
	r, f := likeRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Поставь лайк", false) {
		t.Fatal("лайк не перехвачен")
	}
	calls := f.waitCalls(t, 1)
	if calls[0] != "PUT /me/library?uris=spotify%3Atrack%3A1" {
		t.Fatalf("обращение к API: %v", calls)
	}
	waitFor(t, "лайк учтён", func() bool { return r.c.voiceStatus().Likes == 1 })
	waitFor(t, "диалог Алисы отменён", func() bool { return r.cancels.Load() == 1 })
	r.noCommand(t) // Soloist лайк не касается
	if st := r.c.voiceStatus(); !st.LikeOK || st.Handled != 1 {
		t.Fatalf("статистика: %+v", st)
	}
}

func TestFastLikeKeepsTheDialog(t *testing.T) {
	r, f := likeRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Лайк", true) {
		t.Fatal("быстрый лайк не перехвачен")
	}
	f.waitCalls(t, 1)
	time.Sleep(60 * time.Millisecond)
	if r.cancels.Load() != 0 {
		t.Fatal("диалог быстрой команды отменён")
	}
	if r.mach.armed.Load() != 0 {
		t.Fatal("после лайка платформа ничего не запускает, а запуск отдан Spotify")
	}
}

func TestUnlikeDeletes(t *testing.T) {
	r, f := likeRig(t, nil)
	r.state(t, "paused")
	r.setOwner(ownerSpotify)
	if !r.c.InterceptPhrase(reqID, "Убери лайк", false) {
		t.Fatal("снятие лайка не перехвачено")
	}
	if calls := f.waitCalls(t, 1); !strings.HasPrefix(calls[0], "DELETE /me/library") {
		t.Fatalf("обращение: %v", calls)
	}
}

func TestLikeNotInterceptedWhenItCannotBeDone(t *testing.T) {
	t.Run("нет входа", func(t *testing.T) {
		r := newVoiceRig(t, func(c *Config) { c.ClientID = testClientID })
		r.state(t, "playing")
		if r.c.InterceptPhrase(reqID, "Лайк", false) {
			t.Fatal("лайк перехвачен без входа в Web API: Алиса осталась бы без ответа")
		}
		if r.cancels.Load() != 0 {
			t.Fatal("диалог отменён зря")
		}
	})
	t.Run("лайки выключены", func(t *testing.T) {
		r, f := likeRig(t, func(c *Config) { c.Like = false })
		r.state(t, "playing")
		if r.c.InterceptPhrase(reqID, "Лайк", false) {
			t.Fatal("like = false, а лайк перехвачен")
		}
		time.Sleep(50 * time.Millisecond)
		if len(f.callLog()) != 0 {
			t.Fatal("обращение к API при like = false")
		}
	})
	t.Run("играет Яндекс", func(t *testing.T) {
		r, f := likeRig(t, nil)
		r.state(t, "playing")
		r.mach.playing.Store(true)
		if r.c.InterceptPhrase(reqID, "Лайк", false) {
			t.Fatal("лайк Spotify перехвачен, пока играет Яндекс")
		}
		time.Sleep(50 * time.Millisecond)
		if len(f.callLog()) != 0 {
			t.Fatal("обращение к API, когда играет Яндекс")
		}
	})
	t.Run("играет не трек", func(t *testing.T) {
		r, _ := likeRig(t, nil)
		r.srv.push(t, evAuth)
		r.srv.push(t, `{"type":"playback_state","status":"playing","item":{"uri":"spotify:episode:9"}}`)
		waitFor(t, "эпизод", func() bool { return r.c.ctl.state().URI == "spotify:episode:9" })
		if r.c.InterceptPhrase(reqID, "Лайк", false) {
			t.Fatal("лайк перехвачен для подкаста")
		}
	})
}

func TestLikeFromCloudDirective(t *testing.T) {
	r, f := likeRig(t, nil)
	r.state(t, "playing")
	r.c.OnDirective("player_like")
	if calls := f.waitCalls(t, 1); !strings.HasPrefix(calls[0], "PUT /me/library") {
		t.Fatalf("обращение: %v", calls)
	}
	// фраза уже выполнила лайк, а облако всё равно прислало директиву: повтор не нужен
	r2, f2 := likeRig(t, nil)
	r2.state(t, "playing")
	r2.c.InterceptPhrase(reqID, "Лайк", true)
	f2.waitCalls(t, 1)
	r2.c.OnDirective("player_like")
	time.Sleep(80 * time.Millisecond)
	if n := len(f2.callLog()); n != 1 {
		t.Fatalf("директива повторила лайк: обращений %d", n)
	}
}

func TestLikeFailureIsReportedAndNotCounted(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.status["/v1/me/library"] = http.StatusInternalServerError
	f.mu.Unlock()
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Лайк", false) {
		t.Fatal("лайк не перехвачен")
	}
	f.waitCalls(t, 1)
	time.Sleep(80 * time.Millisecond)
	if r.c.voiceStatus().Likes != 0 {
		t.Fatal("неудавшийся лайк засчитан")
	}
}

func TestBeepGoesThroughTheEngine(t *testing.T) {
	for _, ok := range []bool{true, false} {
		eng, o := newEngine()
		c := newComponent(*defaults(), eng, nil, func(string, ...any) {}, t.TempDir())
		c.beep(ok)
		want := len(confirmTone(ok, 48000))
		waitFor(t, "сигнал дошёл до вывода", func() bool { return o.total() >= want })
		if o.total() > want+4*480 {
			t.Fatalf("ok=%v: в вывод ушло %d байт, ждали около %d", ok, o.total(), want)
		}
	}
	(&Component{}).beep(true) // без движка — ничего, без паники
}

func TestConfirmToneShapeAndLoudness(t *testing.T) {
	good, bad := confirmTone(true, 48000), confirmTone(false, 48000)
	if len(good) != 48000*4/5 { // 0,2 с
		t.Fatalf("сигнал «готово» %d байт", len(good))
	}
	if len(bad) != int(48000*0.26)*4 {
		t.Fatalf("сигнал «не вышло» %d байт", len(bad))
	}
	for name, b := range map[string][]byte{"готово": good, "не вышло": bad} {
		var peak int
		for i := 0; i+1 < len(b); i += 2 {
			v := int(int16(uint16(b[i]) | uint16(b[i+1])<<8))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if peak < 4000 || peak > 11000 {
			t.Fatalf("%s: пик %d (ждали заметный, но не громкий сигнал)", name, peak)
		}
		if b[0] != 0 || b[1] != 0 { // старт с нуля: без щелчка
			t.Fatalf("%s: сигнал начинается не с тишины", name)
		}
	}
}

func TestClientIDValidation(t *testing.T) {
	c := defaults()
	if err := c.validate(); err != nil {
		t.Fatalf("пустой client_id (лайки не настроены) должен проходить: %v", err)
	}
	c.ClientID = testClientID
	if err := c.validate(); err != nil {
		t.Fatalf("нормальный client_id отвергнут: %v", err)
	}
	for _, bad := range []string{"короткий", "с пробелом 0123456789abcdef0123456789", "a&b=c0123456789abcdef0123456789"} {
		c.ClientID = bad
		if err := c.validate(); err == nil {
			t.Errorf("client_id %q принят", bad)
		}
	}
}
