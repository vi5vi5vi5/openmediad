package spotifysoloist

import (
	"context"
	"sync"
	"testing"
	"time"

	"openmediad/internal/libs/volmap"
)

// fakeMaster — регулятор колонки 0…255 (таблица bergamot: уровень 0–20 → 0, 33 … 255).
type fakeMaster struct {
	mu      sync.Mutex
	cur     int
	changes chan struct{}
	closed  bool
}

func newFakeMaster(cur int) *fakeMaster {
	return &fakeMaster{cur: cur, changes: make(chan struct{}, 4)}
}

func (f *fakeMaster) Level() (int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur, 255, !f.closed
}
func (f *fakeMaster) Changes() <-chan struct{} { return f.changes }
func (f *fakeMaster) Close()                   { f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *fakeMaster) external(level int) { // платформа поставила уровень (голос, кнопки, наш запрос «громкость N»)
	f.mu.Lock()
	f.cur = volmap.Default().Master(level)
	f.mu.Unlock()
	f.changes <- struct{}{}
}

// fakePlatform принимает «громкость N» и ставит уровень 2N, как платформа.
type fakePlatform struct {
	mu   sync.Mutex
	asks []int
	m    *fakeMaster
}

func (p *fakePlatform) ask(n int) error {
	p.mu.Lock()
	p.asks = append(p.asks, n)
	p.mu.Unlock()
	p.m.external(2 * n)
	return nil
}
func (p *fakePlatform) asksCopy() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.asks...)
}

// fakeVol — громкость Soloist: SetVolume присылает событие (эхо), ползунок приложения — slider().
type fakeVol struct {
	mu     sync.Mutex
	st     playerState
	sets   []int
	vs     *volumeSync
	noEcho bool
}

func (f *fakeVol) state() playerState { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *fakeVol) SetVolume(v int) error {
	f.mu.Lock()
	f.sets = append(f.sets, v)
	if !f.noEcho {
		f.st.Volume = v
	}
	f.mu.Unlock()
	f.vs.poke()
	return nil
}
func (f *fakeVol) slider(v int) { f.mu.Lock(); f.st.Volume = v; f.mu.Unlock(); f.vs.poke() }
func (f *fakeVol) setsCopy() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.sets...)
}

// volRig: колонка на уровне level (из 20), Spotify в состоянии st.
func volRig(t *testing.T, level int, st playerState) (*fakeMaster, *fakePlatform, *fakeVol, *volumeSync) {
	t.Helper()
	m := newFakeMaster(volmap.Default().Master(level))
	plat := &fakePlatform{m: m}
	s := &fakeVol{st: st}
	v := newVolumeSync(m, volmap.Default(), s, plat.ask, func(string, ...any) {})
	v.idle = 20 * time.Millisecond
	s.vs = v
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { v.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return m, plat, s, v
}

var loggedIn = playerState{Connected: true, LoggedIn: true, Volume: -1}

func TestPercentIsPlatformLevel(t *testing.T) {
	_, _, _, v := volRig(t, 12, loggedIn)
	if p := v.percent(); p != 60 { // уровень 12 из 20
		t.Fatalf("уровень 12 из 20 = %d%%, ждали 60", p)
	}
}

func TestVoiceQuieterMovesSpotifySlider(t *testing.T) {
	m, plat, s, v := volRig(t, 10, loggedIn) // колонка 50%
	s.slider(50)
	waitFor(t, "начальная громкость принята", func() bool { return v.status().SoloistPct == 50 })
	m.external(8) // «Алиса, тише»: уровень 10 → 8
	waitFor(t, "ползунок Spotify подтянулся к колонке", func() bool {
		sets := s.setsCopy()
		return len(sets) > 0 && sets[len(sets)-1] == 40
	})
	if asks := plat.asksCopy(); len(asks) != 0 {
		t.Fatalf("платформу просить не нужно, она сама поменяла уровень: %v", asks)
	}
}

// Ошибка из жизни: ползунок на 30%, платформа «помнила» 100% — после «тише» Spotify прыгал на 90%. Теперь ползунок идёт через платформу,
// и она знает новый уровень.
func TestSliderGoesThroughPlatformSoQuieterStartsFromIt(t *testing.T) {
	m, plat, s, _ := volRig(t, 20, loggedIn) // колонка помнит 100%
	s.slider(100)
	time.Sleep(40 * time.Millisecond)
	s.slider(30) // ползунок в приложении
	waitFor(t, "платформа получила «громкость 3»", func() bool {
		a := plat.asksCopy()
		return len(a) == 1 && a[0] == 3
	})
	waitFor(t, "колонка на уровне 6", func() bool { cur, _, _ := m.Level(); return cur == volmap.Default().Master(6) })
	m.external(4) // «Алиса, тише» от уровня 6, а не от 20: 30% → 20%
	waitFor(t, "ползунок Spotify на 20%", func() bool {
		sets := s.setsCopy()
		return len(sets) > 0 && sets[len(sets)-1] == 20
	})
}

func TestSliderSnapsToPlatformStep(t *testing.T) {
	_, plat, s, _ := volRig(t, 10, loggedIn)
	s.slider(50)
	time.Sleep(40 * time.Millisecond)
	s.slider(33) // платформа умеет шаг 10%: получится 30, ползунок подтянется
	waitFor(t, "ползунок подтянут к 30%", func() bool {
		sets := s.setsCopy()
		return len(sets) > 0 && sets[len(sets)-1] == 30
	})
	if a := plat.asksCopy(); len(a) != 1 || a[0] != 3 {
		t.Fatalf("запросы платформе %v, ждали «громкость 3»", a)
	}
}

func TestSliderDragIsDebounced(t *testing.T) {
	_, plat, s, _ := volRig(t, 10, loggedIn)
	s.slider(50)
	time.Sleep(40 * time.Millisecond)
	for _, p := range []int{55, 60, 65, 70, 75, 80} { // тянут быстрее порога покоя
		s.slider(p)
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, "один запрос в конце", func() bool { return len(plat.asksCopy()) >= 1 })
	time.Sleep(80 * time.Millisecond)
	if a := plat.asksCopy(); len(a) != 1 || a[0] != 8 {
		t.Fatalf("запросы платформе %v, ждали один «громкость 8»", a)
	}
}

func TestNoRequestWhenLevelAlreadyMatches(t *testing.T) {
	_, plat, s, _ := volRig(t, 6, loggedIn) // 30%
	s.slider(30)
	time.Sleep(40 * time.Millisecond)
	s.slider(32) // тот же шаг платформы
	time.Sleep(100 * time.Millisecond)
	if a := plat.asksCopy(); len(a) != 0 {
		t.Fatalf("лишний запрос платформе: %v", a)
	}
}

func TestAlignsSpotifyToSpeakerAfterLogin(t *testing.T) {
	_, _, s, _ := volRig(t, 10, loggedIn) // колонка 50%, Spotify показывает 100%
	s.slider(100)
	waitFor(t, "Spotify приведён к громкости колонки", func() bool {
		sets := s.setsCopy()
		return len(sets) == 1 && sets[0] == 50
	})
}

func TestNothingHappensBeforeLogin(t *testing.T) {
	m, plat, s, v := volRig(t, 10, playerState{Connected: true, Volume: -1}) // не вошёл
	m.external(4)
	v.poke()
	time.Sleep(80 * time.Millisecond)
	if len(s.setsCopy()) != 0 || len(plat.asksCopy()) != 0 {
		t.Fatalf("до входа громкость трогать нельзя: soloist=%v платформа=%v", s.setsCopy(), plat.asksCopy())
	}
}

// Soloist наши записи не повторяет (команда потерялась): платформу из-за этого просить не нужно.
func TestNoEchoFromSoloistDoesNotMovePlatform(t *testing.T) {
	_, plat, s, v := volRig(t, 10, loggedIn)
	s.noEcho = true
	s.slider(100) // первое сообщение: Spotify 100%, колонка 50% → Spotify просят понизиться, но он молчит
	waitFor(t, "Spotify попросили подстроиться", func() bool { return len(s.setsCopy()) == 1 })
	for i := 0; i < 5; i++ { // дальше лишние события (позиция, статус): громкость в них прежняя
		v.poke()
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	if a := plat.asksCopy(); len(a) != 0 {
		t.Fatalf("платформу попросили по устаревшим данным: %v", a)
	}
}

func TestExpectationWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	var e expectation
	e.set(40, now)
	if e.consume(45, now) {
		t.Fatal("чужое значение принято за эхо")
	}
	if !e.consume(41, now.Add(time.Second)) {
		t.Fatal("эхо с округлением на единицу не узнано")
	}
	if e.consume(41, now.Add(time.Second)) {
		t.Fatal("эхо засчитано дважды")
	}
	e.set(40, now)
	if e.consume(40, now.Add(3*time.Second)) {
		t.Fatal("устаревшее ожидание сработало")
	}
}
