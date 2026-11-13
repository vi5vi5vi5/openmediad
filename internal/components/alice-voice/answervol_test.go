package alicevoice

import (
	"sync"
	"testing"
	"time"
)

// fakeMaster — регулятор колонки в памяти: platform() меняет громкость, как платформа; Set — запись компонента.
type fakeMaster struct {
	mu      sync.Mutex
	cur     int
	sets    []int
	changes chan struct{}
	closed  bool
}

func newFakeMaster(cur int) *fakeMaster {
	return &fakeMaster{cur: cur, changes: make(chan struct{}, 1)}
}

func (m *fakeMaster) Level() (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, 255, true
}
func (m *fakeMaster) Changes() <-chan struct{} { return m.changes }
func (m *fakeMaster) Close()                   { m.mu.Lock(); m.closed = true; m.mu.Unlock() }
func (m *fakeMaster) notify() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}
func (m *fakeMaster) Set(v int) error {
	m.mu.Lock()
	m.cur = v
	m.sets = append(m.sets, v)
	m.mu.Unlock()
	m.notify()
	return nil
}
func (m *fakeMaster) platform(v int) {
	m.mu.Lock()
	m.cur = v
	m.mu.Unlock()
	m.notify()
}
func (m *fakeMaster) level() int { v, _, _ := m.Level(); return v }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

// settle даёт горутине удержания разобрать уже отправленные события.
func settle() { time.Sleep(30 * time.Millisecond) }

func newTestKeeper(t *testing.T, m *fakeMaster) *answerKeeper {
	k := newAnswerKeeper(m, t.Logf)
	t.Cleanup(k.Close)
	return k
}

// Обычный ответ: платформа подняла громкость в тот же миг, когда пришёл ответ облака без директив громкости. Громкость возвращается,
// какое бы событие ни пришло первым (ответ или перемена громкости: разница между ними — миллисекунды).
func TestKeeperRevertsPlatformBoost(t *testing.T) {
	for _, replyFirst := range []bool{true, false} {
		m := newFakeMaster(68)
		k := newTestKeeper(t, m)
		k.OnFocus(true)
		settle()
		if replyFirst {
			k.OnReply([]string{"alarm_add_directive", "tts_play_placeholder"})
			settle()
			m.platform(115)
		} else {
			m.platform(115)
			settle()
			k.OnReply([]string{"tts_play_placeholder"})
		}
		eventually(t, "громкость возвращена", func() bool { return m.level() == 68 })
		// ответ кончился: платформа возвращает громкость сама — это та же громкость, ничего не происходит
		k.OnFocus(false)
		m.platform(68)
		settle()
		if st := k.status(); st.Base != 68 || st.Reverted != 1 || st.Accepted != 0 {
			t.Fatalf("ответ первым=%v: состояние %+v, ждали base 68, один возврат", replyFirst, st)
		}
	}
}

// Двухходовой диалог: между ответами платформа возвращает громкость и поднимает снова — оба подъёма возвращаются.
func TestKeeperHoldsEveryAnswerOfDialog(t *testing.T) {
	m := newFakeMaster(68)
	k := newTestKeeper(t, m)
	k.OnFocus(true)
	for i := 0; i < 2; i++ {
		settle()
		k.OnReply([]string{"tts_play_placeholder"})
		settle()
		m.platform(115)
		eventually(t, "громкость возвращена", func() bool { return m.level() == 68 })
		time.Sleep(replyWindow + 50*time.Millisecond) // ответ звучит, окно закрылось
	}
	if st := k.status(); st.Reverted != 2 || st.Base != 68 {
		t.Fatalf("состояние %+v, ждали два возврата", st)
	}
}

// «Алиса, громче»: в ответе есть директива громкости — перемена принимается и становится новой громкостью.
func TestKeeperAcceptsVoiceVolumeRequest(t *testing.T) {
	for _, replyFirst := range []bool{true, false} {
		m := newFakeMaster(68)
		k := newTestKeeper(t, m)
		k.OnFocus(true)
		settle()
		if replyFirst {
			k.OnReply([]string{"sound_louder", "tts_play_placeholder"})
			settle()
			m.platform(91)
		} else {
			m.platform(91)
			settle()
			k.OnReply([]string{"sound_set_level"})
		}
		eventually(t, "новая громкость принята", func() bool { return k.status().Base == 91 })
		if len(m.sets) != 0 || m.level() != 91 {
			t.Fatalf("ответ первым=%v: громкость, о которой попросили, трогать нельзя (записи %v, сейчас %d)", replyFirst, m.sets, m.level())
		}
	}
}

// Кнопки колонки посреди диалога: ответа облака рядом нет — перемена принимается.
func TestKeeperAcceptsButtonsDuringDialog(t *testing.T) {
	m := newFakeMaster(68)
	k := newTestKeeper(t, m)
	k.OnFocus(true)
	settle()
	m.platform(80)
	eventually(t, "громкость с кнопок принята", func() bool { return k.status().Base == 80 })
	if len(m.sets) != 0 {
		t.Fatalf("громкость с кнопок вернули: %v", m.sets)
	}
}

// Вне диалога компонент громкость не трогает: любая перемена — новая громкость человека.
func TestKeeperFollowsVolumeOutsideDialog(t *testing.T) {
	m := newFakeMaster(68)
	k := newTestKeeper(t, m)
	m.platform(140)
	eventually(t, "громкость принята", func() bool { return k.status().Base == 140 })
	// старый ответ облака (диалог давно кончился) не делает следующую перемену «платформенной»
	k.OnReply([]string{"tts_play_placeholder"})
	settle()
	m.platform(91)
	eventually(t, "громкость принята", func() bool { return k.status().Base == 91 })
	if len(m.sets) != 0 {
		t.Fatalf("вне диалога громкость возвращать нельзя: %v", m.sets)
	}
}

// Платформа ставит громкость в несколько шагов: после возврата возвращаются и следующие шаги.
func TestKeeperRevertsSteppedBoost(t *testing.T) {
	m := newFakeMaster(68)
	k := newTestKeeper(t, m)
	k.OnFocus(true)
	settle()
	k.OnReply(nil)
	settle()
	for _, v := range []int{80, 100, 115} {
		m.platform(v)
		eventually(t, "шаг возвращён", func() bool { return m.level() == 68 })
	}
	if st := k.status(); st.Base != 68 || st.Accepted != 0 {
		t.Fatalf("состояние %+v", st)
	}
}

func TestKeeperCloseReleasesMaster(t *testing.T) {
	m := newFakeMaster(68)
	k := newAnswerKeeper(m, nil)
	k.Close()
	k.Close() // повторно — безопасно
	k.OnFocus(true)
	k.OnReply(nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		t.Fatal("регулятор надо закрыть")
	}
}

func TestVolumeDirective(t *testing.T) {
	for name, want := range map[string]bool{"sound_set_level": true, "sound_louder": true, "sound_quiter": true, "volume_set": true,
		"tts_play_placeholder": false, "audio_play": false, "draw_animation_directive": false} {
		if volumeDirective(name) != want {
			t.Errorf("%s: ждали %v", name, want)
		}
	}
}

// Настройка answer_volume переключается на ходу: hold открывает регулятор и держит громкость, platform закрывает его и больше ничего не трогает.
func TestAnswerVolumeSwitchesOnReload(t *testing.T) {
	m := newFakeMaster(68)
	old := openMaster
	openMaster = func() (masterCtl, error) { return m, nil }
	t.Cleanup(func() { openMaster = old })
	b := &Backend{Logf: t.Logf}
	t.Cleanup(b.Shutdown)
	if b.DebugState().(map[string]any)["answer_volume"] != AnswerVolumePlatform {
		t.Fatal("по умолчанию громкость решает колонка")
	}
	if r := b.Reload(&Config{AnswerVolume: AnswerVolumeHold}); len(r) != 0 {
		t.Fatalf("перезапуск не нужен: %v", r)
	}
	b.OnFocus(true)
	settle()
	b.OnReply("req", []string{"tts_play_placeholder"})
	settle()
	m.platform(115)
	eventually(t, "громкость возвращена", func() bool { return m.level() == 68 })
	b.Reload(&Config{AnswerVolume: "как-нибудь"}) // неверное значение ничего не меняет
	if b.keeper.Load() == nil {
		t.Fatal("неверная настройка не должна выключать удержание")
	}
	b.Reload(&Config{AnswerVolume: AnswerVolumePlatform})
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed || b.keeper.Load() != nil {
		t.Fatal("platform: регулятор надо закрыть")
	}
	b.OnReply("req2", nil)
	m.platform(115)
	settle()
	if m.level() != 115 {
		t.Fatal("после выключения громкость трогать нельзя")
	}
}
