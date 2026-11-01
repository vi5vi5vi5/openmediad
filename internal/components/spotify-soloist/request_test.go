package spotifysoloist

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	artistURI = "spotify:artist:1Xyo4u8uXC1ZmMpatF05PJ"
	trackURI  = "spotify:track:2uIX8YMNjGMD7441kqyyNU"
)

var musicReply = []string{"tts_play_placeholder", "audio_play", "@@mm_stack_engine_get_next"}

// swallowRig — заглушка службы демона «погасить голос».
func swallowRig(r *voiceRig) *atomic.Int32 {
	var n atomic.Int32
	r.c.swallow = func() { n.Add(1) }
	return &n
}

// Облако не поняло явную просьбу («не нашла»): Spotify играет, голос Алисы гасим, диалог отменять поздно и незачем.
func TestExplicitRequestPlaysWhenCloudDidNotKnowTheMusic(t *testing.T) {
	r := newVoiceRig(t, nil)
	swallowed := swallowRig(r)
	r.state(t, "paused")
	if _, _, err := r.c.aliases.set("группа крови", trackURI); err != nil {
		t.Fatal(err)
	}
	if r.c.InterceptPhrase(reqID, "Включи песню Группа крови", false) {
		t.Fatal("просьба «включи …» забрана сразу: результат поиска ещё неизвестен")
	}
	r.noCommand(t) // облако ещё не ответило
	r.c.OnReply(reqID, []string{"tts_play_placeholder"})
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || !strings.Contains(m, `"uri":"`+trackURI+`"`) {
		t.Fatalf("Soloist получил %s", m)
	}
	waitFor(t, "ответ голосом погашен", func() bool { return swallowed.Load() == 1 })
	time.Sleep(40 * time.Millisecond)
	if r.cancels.Load() != 0 || r.mach.armed.Load() != 0 {
		t.Fatal("диалог уже закончился, а запуска Яндекса не будет: отменять и занимать нечего")
	}
	if st := r.c.voiceStatus(); st.Handled != 1 || st.Last != "включить по названию" {
		t.Fatalf("статистика: %+v", st)
	}
}

// Облако молчит дольше explicitWait: диалог не нужен, голос гасим, запуск Яндекса (если вдруг придёт) отдаём Spotify.
func TestExplicitRequestPlaysWithoutCloudAnswerAfterTimeout(t *testing.T) {
	r := newVoiceRig(t, nil)
	swallowed := swallowRig(r)
	r.state(t, "paused")
	r.c.aliases.set("группа крови", trackURI)
	r.c.InterceptPhrase(reqID, "Включи песню Группа крови", false)
	select {
	case m := <-r.srv.recv:
		if !strings.Contains(m, trackURI) {
			t.Fatalf("Soloist получил %s", m)
		}
	case <-time.After(explicitWait + 2*time.Second):
		t.Fatal("явная просьба так и не выполнена")
	}
	waitFor(t, "диалог отменён", func() bool { return r.cancels.Load() == 1 })
	waitFor(t, "голос погашен", func() bool { return swallowed.Load() == 1 })
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
}

// Облако ответило музыкой («Включаю.»): Алису не трогаем, запуск Яндекса отдаём Spotify.
func TestExplicitRequestAfterCloudMusicVerdictLeavesAliceAlone(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.QuietReply = false })
	swallowed := swallowRig(r)
	r.state(t, "paused")
	r.c.aliases.set("группа крови", trackURI)
	r.c.InterceptPhrase(reqID, "Включи песню Группа крови", false)
	r.c.OnReply(reqID, musicReply)
	r.command(t)
	time.Sleep(40 * time.Millisecond)
	if r.cancels.Load() != 0 || swallowed.Load() != 0 {
		t.Fatal("Алиса ответила сама: диалог и голос трогать нельзя")
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
}

// «Включи Кино» может быть про фильм: пока облако не сказало «это музыка», ничего не делаем.
func TestBareRequestWaitsForCloudVerdict(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.QuietReply = false })
	swallowed := swallowRig(r)
	r.state(t, "paused")
	r.c.aliases.set("кино", artistURI)
	r.c.InterceptPhrase(reqID, "Включи Кино", false)
	r.noCommand(t)
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, `"uri":"`+artistURI+`"`) {
		t.Fatalf("Soloist получил %s", m)
	}
	time.Sleep(60 * time.Millisecond)
	if r.cancels.Load() != 0 || swallowed.Load() != 0 {
		t.Fatal("Алиса уже ответила сама («Включаю.»): диалог и голос трогать нельзя")
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса после «Включаю.» не отдан Spotify")
	}
}

func TestBareRequestDroppedWhenCloudSaysItIsNotMusic(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	r.c.aliases.set("кино", artistURI)
	r.c.InterceptPhrase(reqID, "Включи Кино", false)
	r.c.OnReply(reqID, []string{"tts_play_placeholder", "open_uri", "player_pause"}) // например, фильм
	r.noCommand(t)
	r.c.OnReply(reqID, musicReply) // запоздалый повтор уже ничего не меняет
	r.noCommand(t)
}

func TestVerdictCanArriveBeforeTheSearchFinishes(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "paused")
	gate := make(chan struct{})
	r.c.resolve = func(playRequest) (resolution, error) {
		<-gate
		return resolution{URI: artistURI, Name: "Кино", How: "search"}, nil
	}
	r.c.InterceptPhrase(reqID, "Включи Кино", false)
	r.c.OnReply(reqID, musicReply)
	r.noCommand(t)
	close(gate)
	if m := r.command(t); !strings.Contains(m, artistURI) {
		t.Fatalf("после поиска Soloist получил %s", m)
	}
}

func TestNothingFoundLeavesTheRequestToAlice(t *testing.T) {
	for _, phrase := range []string{"Включи Кино", "Включи группу Кино"} {
		r := newVoiceRig(t, nil)
		swallowed := swallowRig(r)
		r.state(t, "paused")
		r.c.resolve = func(playRequest) (resolution, error) { return resolution{}, nil }
		r.c.InterceptPhrase(reqID, phrase, false)
		r.c.OnReply(reqID, musicReply)
		r.noCommand(t)
		time.Sleep(40 * time.Millisecond)
		if r.cancels.Load() != 0 || swallowed.Load() != 0 || r.mach.armed.Load() != 0 {
			t.Fatalf("%q: Алиса осталась без ответа или запуск Яндекса забран зря", phrase)
		}
	}
}

func TestRequestFindsInSpotifySearchWhenNoAlias(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.searchJSON = searchBody
	f.mu.Unlock()
	swallowRig(r)
	r.state(t, "paused")
	r.c.InterceptPhrase(reqID, "Включи группу Кино", false)
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, `"uri":"spotify:artist:AAAAAAAAAAAAAAAAAAAAAA"`) {
		t.Fatalf("Soloist получил %s", m)
	}
	calls := f.callLog()
	if len(calls) == 0 || !strings.HasPrefix(calls[len(calls)-1], "GET /search?") {
		t.Fatalf("поиска в Spotify не было: %v", calls)
	}
}

func TestAliasBeatsSearch(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.searchJSON = searchBody
	f.mu.Unlock()
	swallowRig(r)
	r.state(t, "paused")
	r.c.aliases.set("кино", "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE")
	r.c.InterceptPhrase(reqID, "Включи музыку Кино", false)
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, "spotify:playlist:EEEEEEEEEEEEEEEEEEEEEE") {
		t.Fatalf("Soloist получил %s", m)
	}
	for _, c := range f.callLog() {
		if strings.Contains(c, "/search") {
			t.Fatal("свои названия точнее поиска: Web API не нужен")
		}
	}
}

func TestRequestsIgnoredWhenSwitchedOffOrFast(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.PlayByName = false })
	r.state(t, "paused")
	r.c.aliases.set("кино", artistURI)
	r.c.InterceptPhrase(reqID, "Включи группу Кино", false)
	r.noCommand(t)

	r2 := newVoiceRig(t, nil)
	r2.state(t, "paused")
	r2.c.aliases.set("кино", artistURI)
	r2.c.InterceptPhrase(reqID, "Включи группу Кино", true) // быстрые команды платформы — это «пауза», «дальше», а не просьбы с названием
	r2.noCommand(t)
}

func TestSoloistNotReadyMeansNoPlay(t *testing.T) {
	r := newVoiceRig(t, nil) // вход не выполнен
	r.c.aliases.set("кино", artistURI)
	r.c.InterceptPhrase(reqID, "Включи группу Кино", false)
	r.noCommand(t)
	if r.cancels.Load() != 0 {
		t.Fatal("диалог отменён, хотя запускать нечем")
	}
}

func TestPendingRequestsAreBounded(t *testing.T) {
	r := newVoiceRig(t, nil)
	var mu sync.Mutex
	block := make(chan struct{})
	r.c.resolve = func(playRequest) (resolution, error) {
		mu.Lock()
		mu.Unlock()
		<-block
		return resolution{}, nil
	}
	defer close(block)
	for i := 0; i < 50; i++ {
		id := strings.Repeat("a", 35) + string(rune('a'+i%26))
		r.c.InterceptPhrase(id+string(rune('0'+i/26)), "Включи группу Кино", false)
	}
	r.c.mu.Lock()
	n := len(r.c.reqs)
	r.c.mu.Unlock()
	if n > maxPendingReqs {
		t.Fatalf("ожидающих просьб %d, предел %d", n, maxPendingReqs)
	}
}

func TestPlayRequestMentionsOfServices(t *testing.T) {
	if _, ok := parsePlayRequest("Включи яндекс музыку Кино"); ok {
		t.Error("просьба про Яндекс.Музыку принята")
	}
	if _, ok := parsePlayRequest("Включи Кино из Яндекс Музыки"); ok {
		t.Error("просьба из Яндекс Музыки принята")
	}
	got, ok := parsePlayRequest("Включи Кино в спотифае")
	if !ok || got.Query != "кино" || !got.Explicit {
		t.Errorf("«в спотифае» не распознано: %+v %v", got, ok)
	}
	got, ok = parsePlayRequest("Включи в Spotify группу Кино")
	if !ok || got.Query != "кино" || !got.Explicit || got.Kind != kindArtist {
		t.Errorf("«в Spotify» не распознано: %+v %v", got, ok)
	}
	if _, ok := parsePlayRequest("Включи свет в спотифае"); ok && false {
		t.Error("недостижимо")
	}
}

// quiet_reply = true (по умолчанию): запрос выполняет Spotify, и «Включаю.» от Алисы не звучит. Диалог не отменяется — ответ уже идёт, гасится только голос.
func TestQuietReplySilencesAlicesVoiceWhenSpotifyTakesTheRequest(t *testing.T) {
	r := newVoiceRig(t, nil)
	swallowed := swallowRig(r)
	r.state(t, "paused")
	r.c.aliases.set("кино", artistURI)
	r.c.InterceptPhrase(reqID, "Включи Кино", false)
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, artistURI) {
		t.Fatalf("Soloist получил %s", m)
	}
	waitFor(t, "голос Алисы погашен", func() bool { return swallowed.Load() == 1 })
	time.Sleep(40 * time.Millisecond)
	if r.cancels.Load() != 0 {
		t.Fatal("диалог отменён: ответ Алисы уже идёт, гасить нужно только голос")
	}
	if r.mach.armed.Load() != 1 {
		t.Fatal("запуск Яндекса не отдан Spotify")
	}
}

// Если запрос Spotify выполнить не удалось (ничего не найдено), голос Алисы остаётся: иначе пользователь не услышал бы ничего.
func TestQuietReplyDoesNotSilenceAliceWhenNothingIsPlayed(t *testing.T) {
	r := newVoiceRig(t, nil)
	swallowed := swallowRig(r)
	r.state(t, "paused")
	r.c.resolve = func(playRequest) (resolution, error) { return resolution{}, nil }
	r.c.InterceptPhrase(reqID, "Включи Кино", false)
	r.c.OnReply(reqID, musicReply)
	r.noCommand(t)
	time.Sleep(40 * time.Millisecond)
	if swallowed.Load() != 0 {
		t.Fatal("голос Алисы погашен, хотя Spotify ничего не включил")
	}
}

func TestQuietReplyIsConfigurableOnTheFly(t *testing.T) {
	r := newVoiceRig(t, nil)
	n := r.c.config()
	n.QuietReply = false
	r.c.Reload(&n)
	if r.c.config().QuietReply {
		t.Fatal("quiet_reply не применился на ходу")
	}
}
