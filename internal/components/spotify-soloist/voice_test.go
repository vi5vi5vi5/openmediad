package spotifysoloist

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhraseGrammar(t *testing.T) {
	for text, want := range map[string]action{
		"Пауза":  actPause,
		"пауза.": actPause,
		"Поставь на паузу, пожалуйста!": actPause,
		"Останови музыку":               actPause,
		"Продолжи":                      actResume,
		"Сними с паузы":                 actResume,
		"Дальше":                        actNext,
		"Следующая песня":               actNext,
		"Включи следующий трек":         actNext,
		"Пропусти":                      actNext,
		"Назад":                         actPrev,
		"Предыдущий трек":               actPrev,
		"Включи предыдущую песню":       actPrev,
		"  ДАЛЬШЕ  ":                    actNext,
		"Вернись":                       actPrev,
	} {
		if got := parsePhrase(text); got != want {
			t.Errorf("%q → %v, ждали %v", text, got, want)
		}
	}
}

// Грамматика узкая: хвосты и похожие просьбы не перехватываются, их слышит Алиса.
func TestPhraseGrammarRejectsLookalikes(t *testing.T) {
	for _, text := range []string{
		"", "Алиса", "Что дальше", "Дальше что", "Включи следующую песню группы кино", "Пауза на пять минут", "Поставь таймер на паузу",
		"Продолжи сказку", "Продолжи рассказ про волка", "Включи музыку", "Включи Кино", "Стоп машина", "Останови будильник",
		"Стоп", "Алиса, стоп", "Останови", "Остановись", // так же останавливают звонок будильника и таймера: это фраза Алисы
		"Выключи свет", "Назад в будущее", "Включи назад в будущее", "Сколько времени", "Громче", "Тише", "Что играет", "Дальше по плану",
	} {
		if got := parsePhrase(text); got != actNone {
			t.Errorf("%q принято за %v, а это не наша фраза", text, got)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Алиса, ПАУЗА!!":        "пауза",
		"Ёлка":                  "елка",
		"поставь  на   паузу":   "поставь на паузу",
		"Яндекс, пожалуйста...": "",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, ждали %q", in, got, want)
		}
	}
}

// voiceRig — компонент с живым управляющим соединением к поддельному Soloist (состояние задают события) и счётчиком отмен диалога.
type voiceRig struct {
	c       *Component
	srv     *wsServer
	mach    *fakeMach
	cancels atomic.Int32
}

func newVoiceRig(t *testing.T, edit func(*Config)) *voiceRig {
	t.Helper()
	srv := startWS(t)
	r := &voiceRig{srv: srv, mach: &fakeMach{}}
	cfg := *defaults()
	if edit != nil {
		edit(&cfg)
	}
	r.c = newComponent(cfg, nil, r.mach, func(string, ...any) {}, t.TempDir())
	r.c.pcm = &pcmServer{} // без сессий: звучит ли Spotify, видно по состоянию Soloist
	r.c.cancelFn = func(context.Context) error { r.cancels.Add(1); return nil }
	r.c.ctl = newControl(srv.addr(), func(string, ...any) {})
	r.c.ctl.changed = r.c.virtualPush // как в Start: состояние Soloist доходит до автомата
	r.mach.OnVirtualCommand(r.c.onVirtualCommand)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.c.ctl.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "соединение с Soloist", func() bool { return r.c.ctl.state().Connected })
	<-srv.recv // get_auth_state
	<-srv.recv // get_state
	return r
}

// state присылает событие входа и воспроизведения в нужном статусе.
func (r *voiceRig) state(t *testing.T, status string) {
	t.Helper()
	r.srv.push(t, evAuth)
	r.srv.push(t, strings.Replace(evPlaying, `"status":"playing"`, `"status":"`+status+`"`, 1))
	waitFor(t, "статус "+status, func() bool { st := r.c.ctl.state(); return st.LoggedIn && st.Status == status })
}

func (r *voiceRig) command(t *testing.T) string {
	t.Helper()
	select {
	case m := <-r.srv.recv:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("команда в Soloist не ушла")
		return ""
	}
}

func (r *voiceRig) noCommand(t *testing.T) {
	t.Helper()
	select {
	case m := <-r.srv.recv:
		t.Fatalf("в Soloist ушла лишняя команда: %s", m)
	case <-time.After(150 * time.Millisecond):
	}
}

const reqID = "0b4b6c50-1111-4222-8333-944455556666"

func TestPauseWhileSpotifyPlaying(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Пауза", false) {
		t.Fatal("«пауза» при играющем Spotify не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("в Soloist ушло %s, ждали pause", m)
	}
	waitFor(t, "диалог Алисы отменён", func() bool { return r.cancels.Load() == 1 })
	if st := r.c.voiceStatus(); st.Handled != 1 || st.Last != "пауза" {
		t.Fatalf("статистика голоса: %+v", st)
	}
}

func TestNextAndPrevWhilePlaying(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Дальше", false) || !strings.Contains(r.command(t), `"skip_next"`) {
		t.Fatal("«дальше» не стало skip_next")
	}
	if !r.c.InterceptPhrase(reqID, "Назад", false) || !strings.Contains(r.command(t), `"skip_prev"`) {
		t.Fatal("«назад» не стало skip_prev")
	}
}

func TestNothingInterceptedWhenNotOurs(t *testing.T) {
	t.Run("не из грамматики", func(t *testing.T) {
		r := newVoiceRig(t, nil)
		r.state(t, "playing")
		if r.c.InterceptPhrase(reqID, "Включи следующую песню группы кино", false) || r.c.InterceptPhrase(reqID, "Сколько времени", false) {
			t.Fatal("чужая фраза перехвачена")
		}
		r.noCommand(t)
		if r.cancels.Load() != 0 {
			t.Fatal("диалог отменён зря")
		}
	})
	t.Run("Spotify на паузе: «пауза» — не наша", func(t *testing.T) {
		r := newVoiceRig(t, nil)
		r.state(t, "paused")
		if r.c.InterceptPhrase(reqID, "Пауза", false) {
			t.Fatal("«пауза» перехвачена, хотя Spotify не играет")
		}
		r.noCommand(t)
	})
	t.Run("играет музыка Яндекса", func(t *testing.T) {
		r := newVoiceRig(t, nil)
		r.state(t, "playing")
		r.mach.playing.Store(true)
		if r.c.InterceptPhrase(reqID, "Пауза", false) || r.c.InterceptPhrase(reqID, "Дальше", false) {
			t.Fatal("фраза перехвачена, пока играет Яндекс")
		}
		r.noCommand(t)
	})
	t.Run("не вошли в аккаунт", func(t *testing.T) {
		r := newVoiceRig(t, nil)
		if r.c.InterceptPhrase(reqID, "Пауза", false) {
			t.Fatal("перехват без входа в Spotify")
		}
	})
	t.Run("нет соединения", func(t *testing.T) {
		c := newComponent(*defaults(), nil, &fakeMach{}, func(string, ...any) {}, t.TempDir())
		if c.InterceptPhrase(reqID, "Пауза", false) {
			t.Fatal("перехват в ненастроенном компоненте")
		}
	})
}

func TestResumeBelongsToWhoPlayedLast(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerYandex) // позже играл Яндекс: «продолжи» — его
	if r.c.InterceptPhrase(reqID, "Продолжи", false) {
		t.Fatal("«продолжи» забрал Spotify, хотя последним играл Яндекс")
	}
	r.noCommand(t)
	r.c.setOwner(ownerSpotify)
	if !r.c.InterceptPhrase(reqID, "Продолжи", false) {
		t.Fatal("«продолжи» после паузы Spotify не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || strings.Contains(m, "uri") {
		t.Fatalf("продолжение должно быть play без uri: %s", m)
	}
	r.mach.playing.Store(true) // Яндекс играет: Spotify на паузе не хозяин
	if r.c.InterceptPhrase(reqID, "Продолжи", false) {
		t.Fatal("перехват, пока играет Яндекс")
	}
}

func TestNextWhilePausedIsOurs(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	if !r.c.InterceptPhrase(reqID, "Следующая песня", false) || !strings.Contains(r.command(t), `"skip_next"`) {
		t.Fatal("«следующая» при паузе Spotify не перехвачена")
	}
}

func TestVoiceSwitchesOff(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.Voice = false })
	r.state(t, "playing")
	if r.c.InterceptPhrase(reqID, "Пауза", false) {
		t.Fatal("voice = false, а фраза перехвачена")
	}
	r.noCommand(t)
}

func TestNoCancelWhenDisabled(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.VoiceCancel = false })
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Пауза", false) {
		t.Fatal("фраза не перехвачена")
	}
	r.command(t)
	time.Sleep(100 * time.Millisecond)
	if r.cancels.Load() != 0 {
		t.Fatal("voice_cancel = false, а диалог отменён")
	}
}

// Команда не ушла (Soloist перезапускается) — фраза остаётся Алисе: пусть ответит она, чем тишина.
func TestFailedCommandFallsBackToAlice(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.c.ctl.mu.Lock()
	r.c.ctl.conn = nil // состояние ещё «подключён», а записать некуда: Soloist как раз перезапускается
	r.c.ctl.mu.Unlock()
	if r.c.InterceptPhrase(reqID, "Пауза", false) {
		t.Fatal("фраза перехвачена без соединения с Soloist")
	}
	if r.cancels.Load() != 0 {
		t.Fatal("диалог отменён, хотя команда не выполнена")
	}
}

func TestVoiceConfigReloadsWithoutRestart(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	n := *defaults()
	n.Voice = false
	if restart := r.c.Reload(&n); len(restart) != 0 {
		t.Fatalf("voice требует перезапуска демона: %v", restart)
	}
	if r.c.InterceptPhrase(reqID, "Пауза", false) {
		t.Fatal("после voice = false фразы всё ещё перехватываются")
	}
}

func TestDirectiveFromCloudControlsSpotify(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.c.OnDirective("draw_animation_directive") // чужая директива
	r.c.OnDirective("tts_play_placeholder")
	r.noCommand(t)
	r.c.OnDirective("player_pause")
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("player_pause стала %s", m)
	}
	r.c.OnDirective("player_next_track")
	if m := r.command(t); !strings.Contains(m, `"skip_next"`) {
		t.Fatalf("player_next_track стала %s", m)
	}
	r.c.OnDirective("player_previous_track")
	if m := r.command(t); !strings.Contains(m, `"skip_prev"`) {
		t.Fatalf("player_previous_track стала %s", m)
	}
	if st := r.c.voiceStatus(); st.Handled != 3 || !strings.Contains(st.Last, "директива") {
		t.Fatalf("статистика: %+v", st)
	}
}

func TestDirectiveResumeOnlyForPausedSpotify(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.c.OnDirective("player_continue") // уже играет
	r.noCommand(t)
	r.state(t, "paused")
	r.c.OnDirective("player_continue")
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) {
		t.Fatalf("player_continue стала %s", m)
	}
}

func TestDirectiveIgnoredWhenYandexOwnsTheSound(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.mach.playing.Store(true)
	r.c.OnDirective("player_pause")
	r.noCommand(t)
}

func TestDirectiveIgnoredWhenVoiceIsOff(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.Voice = false })
	r.state(t, "playing")
	r.c.OnDirective("player_pause")
	r.noCommand(t)
}

// Фраза уже выполнена перехватом, а облако (отмена не успела) всё равно ответило директивой: второй раз не выполнять.
func TestDirectiveRightAfterInterceptedPhraseIsAnEcho(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Дальше", false) {
		t.Fatal("фраза не перехвачена")
	}
	r.command(t) // skip_next
	r.c.OnDirective("player_next_track")
	r.noCommand(t) // пропущен один трек, а не два
	r.c.OnDirective("player_pause")
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("директива другого действия должна исполняться: %s", m)
	}
}

// Быстрая команда («дальше» без «Алиса»): платформа сама запустит очередь Яндекса, этот запуск должен достаться Spotify;
// диалог не отменяется (ответ локальный, а его анимацию ждёт пользователь).
func TestFastCommandArmsCarrierAndKeepsDialog(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Дальше", true) {
		t.Fatal("быстрая команда не перехвачена")
	}
	if m := r.command(t); !strings.Contains(m, `"skip_next"`) {
		t.Fatalf("«дальше» стало %s", m)
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
	time.Sleep(100 * time.Millisecond)
	if r.cancels.Load() != 0 {
		t.Fatal("диалог быстрой команды отменён: пропадёт анимация")
	}
}

func TestPauseDoesNotArmCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Пауза", true) {
		t.Fatal("«пауза» не перехвачена")
	}
	r.command(t)
	if r.mach.armed.Load() != 0 {
		t.Fatal("после паузы платформа ничего не запускает, а запуск отдан Spotify")
	}
}

func TestResumeAndPrevArmCarrier(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.setOwner(ownerSpotify)
	if !r.c.InterceptPhrase(reqID, "Играй", true) {
		t.Fatal("«играй» не перехвачена")
	}
	r.command(t)
	r.state(t, "playing")
	if !r.c.InterceptPhrase(reqID, "Назад", false) {
		t.Fatal("«назад» не перехвачена")
	}
	r.command(t)
	if r.mach.armed.Load() != 2 {
		t.Fatalf("отдано запусков Яндекса: %d, ждали 2", r.mach.armed.Load())
	}
}
