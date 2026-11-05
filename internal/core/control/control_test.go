package control

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePlayer — плеер с заданным состоянием; помнит, что ему велели.
type fakePlayer struct {
	mu      sync.Mutex
	refuse  map[Origin]bool // источники, которым плеер не отвечает (настройки компонента)
	st      State
	err     error
	did     []Cmd
	froms   []Origin
	yielded int
}

func ready(playing bool) State {
	return State{Ready: true, Playing: playing, Sounding: playing}
}

func paused() State { return State{Ready: true, Resumable: true, Paused: true} }

func (p *fakePlayer) Accepts(o Origin) bool { return !p.refuse[o] }
func (p *fakePlayer) State() State          { p.mu.Lock(); defer p.mu.Unlock(); return p.st }
func (p *fakePlayer) Do(c Cmd, from Origin) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.did = append(p.did, c)
	p.froms = append(p.froms, from)
	return nil
}
func (p *fakePlayer) Yielded() { p.mu.Lock(); p.yielded++; p.mu.Unlock() }

func (p *fakePlayer) set(s State) { p.mu.Lock(); p.st = s; p.mu.Unlock() }
func (p *fakePlayer) last() (Cmd, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.did) == 0 {
		return "", false
	}
	return p.did[len(p.did)-1], true
}
func (p *fakePlayer) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.did) }

// fakePlatform — музыка Яндекса и автомат.
type fakePlatform struct {
	mu        sync.Mutex
	yandex    bool
	armed     int
	takeovers int
	stopped   int // что вернёт TakeOver
}

func (f *fakePlatform) Playing() bool            { f.mu.Lock(); defer f.mu.Unlock(); return f.yandex }
func (f *fakePlatform) ArmCarrier(time.Duration) { f.mu.Lock(); f.armed++; f.mu.Unlock() }
func (f *fakePlatform) TakeOver() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takeovers++
	f.yandex = false
	return f.stopped
}
func (f *fakePlatform) setYandex(v bool) { f.mu.Lock(); f.yandex = v; f.mu.Unlock() }
func (f *fakePlatform) armedN() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.armed }

type rig struct {
	h    *Hub
	p    *fakePlayer
	plat *fakePlatform
	clk  time.Time
	logs []string
}

func newRig(st State) *rig {
	r := &rig{p: &fakePlayer{st: st}, plat: &fakePlatform{stopped: 1}, clk: time.Unix(1000, 0)}
	r.h = &Hub{Platform: r.plat, Now: func() time.Time { return r.clk }, Logf: func(f string, a ...any) { r.logs = append(r.logs, f) }}
	r.h.Add("spotify", r.p)
	return r
}

func (r *rig) advance(d time.Duration) { r.clk = r.clk.Add(d) }

// Команду получает первый плеер, которому она принадлежит; остальные её уже не видят, а журнал знает, кто выполнил.
func TestDoStopsAtTheFirstPlayerThatHandledIt(t *testing.T) {
	first, second := &fakePlayer{st: ready(true)}, &fakePlayer{st: ready(true)}
	var logged []string
	h := &Hub{OnHandled: func(c Cmd, from Origin, player string) {
		logged = append(logged, player+":"+string(c)+":"+string(from))
	}}
	h.Add("not-ready", &fakePlayer{st: State{Playing: true}})
	h.Add("first", first)
	h.Add("second", second)
	if !h.Do(Pause, Headphones) {
		t.Fatal("команду никто не взял")
	}
	if first.count() != 1 || second.count() != 0 {
		t.Fatalf("первый получил %d, второй %d", first.count(), second.count())
	}
	if len(logged) != 1 || logged[0] != "first:pause:headphones" {
		t.Fatalf("журнал: %v", logged)
	}
}

func TestNilAndEmptyHubTakeNothing(t *testing.T) {
	var h *Hub
	if h.Do(Toggle, Headphones) {
		t.Fatal("пустой указатель принял команду")
	}
	if (&Hub{}).Do(Pause, Speaker) {
		t.Fatal("пустой Hub взял команду")
	}
}

func TestPauseOnlyWhilePlayerPlaysAndYandexIsSilent(t *testing.T) {
	r := newRig(ready(true))
	if !r.h.Do(Pause, Voice) {
		t.Fatal("«пауза» при играющем плеере не взята")
	}
	if c, _ := r.p.last(); c != Pause {
		t.Fatalf("плеер получил %q", c)
	}
	// на паузе — не наша
	r = newRig(paused())
	if r.h.Do(Pause, Voice) || r.p.count() != 0 {
		t.Fatal("«пауза» взята, хотя плеер не играет")
	}
	// Яндекс играет — его
	r = newRig(ready(true))
	r.plat.setYandex(true)
	if r.h.Do(Pause, Voice) || r.h.Do(Next, Voice) || r.p.count() != 0 {
		t.Fatal("команда взята, пока играет Яндекс")
	}
	// нет входа и связи — плеер вне игры
	r = newRig(State{Playing: true})
	if r.h.Do(Pause, Voice) {
		t.Fatal("команда взята неготовым плеером")
	}
}

// «Продолжи» достаётся тому, кто играл последним, и только когда никто не играет.
func TestResumeBelongsToWhoPlayedLast(t *testing.T) {
	r := newRig(paused())
	r.h.SetOwner(OwnerPlatform) // позже играл Яндекс: «продолжи» — его
	if r.h.Do(Resume, Voice) || r.p.count() != 0 {
		t.Fatal("«продолжи» забрал плеер, хотя последним играл Яндекс")
	}
	r.h.SetOwner("spotify")
	if !r.h.Do(Resume, Voice) {
		t.Fatal("«продолжи» после паузы плеера не взята")
	}
	if c, _ := r.p.last(); c != Resume {
		t.Fatalf("плеер получил %q", c)
	}
	r.plat.setYandex(true) // Яндекс играет: плеер на паузе не хозяин
	if r.h.Do(Resume, Voice) {
		t.Fatal("взято, пока играет Яндекс")
	}
	// плеер уже играет — «продолжи» не его
	r2 := newRig(ready(true))
	if r2.h.Do(Resume, Voice) {
		t.Fatal("«продолжи» взято у играющего плеера")
	}
}

func TestNextAndPrevWhilePlayingAndWhilePaused(t *testing.T) {
	for name, st := range map[string]State{"играет": ready(true), "на паузе": paused()} {
		r := newRig(st)
		if !r.h.Do(Next, Voice) || !r.h.Do(Prev, Voice) {
			t.Fatalf("%s: дальше или назад не взяты", name)
		}
		if r.p.did[0] != Next || r.p.did[1] != Prev {
			t.Fatalf("%s: плеер получил %v", name, r.p.did)
		}
	}
	r := newRig(paused())
	r.h.SetOwner(OwnerPlatform)
	if r.h.Do(Next, Voice) {
		t.Fatal("«дальше» после Яндекса взят плеером, стоящим на паузе")
	}
}

func TestToggleChoosesPauseOrResume(t *testing.T) {
	r := newRig(ready(true))
	if !r.h.Do(Toggle, Headphones) {
		t.Fatal("переключатель при играющем плеере не взят")
	}
	if c, _ := r.p.last(); c != Pause {
		t.Fatalf("играющему плееру велели %q", c)
	}
	r.p.set(paused())
	if !r.h.Do(Toggle, Headphones) {
		t.Fatal("переключатель при плеере на паузе не взят")
	}
	if c, _ := r.p.last(); c != Resume {
		t.Fatalf("плееру на паузе велели %q", c)
	}
	r.p.set(State{Ready: true}) // ничего не играет и продолжать нечего
	if r.h.Do(Toggle, Headphones) {
		t.Fatal("переключатель взят, хотя плееру нечего делать")
	}
	r.p.set(ready(true))
	r.plat.setYandex(true)
	if r.h.Do(Toggle, Headphones) {
		t.Fatal("переключатель взят у Яндекса")
	}
}

// Настройки компонента (голос выключен, кнопки выключены) делают плеер глухим к этому источнику, но не к остальным.
func TestPlayerCanRefuseASource(t *testing.T) {
	r := newRig(ready(true))
	r.p.refuse = map[Origin]bool{Voice: true, FastVoice: true, Cloud: true}
	if r.h.Do(Pause, Voice) || r.h.Do(Pause, FastVoice) || r.h.Do(Pause, Cloud) {
		t.Fatal("плеер слушает голос, хотя выключен")
	}
	if !r.h.Do(Toggle, Headphones) {
		t.Fatal("кнопки наушников перестали работать из-за выключенного голоса")
	}
}

// Команда не ушла (Soloist перезапускается) — её не считаем выполненной: фраза остаётся Алисе, лучше она ответит, чем тишина.
func TestFailedCommandIsNotHandled(t *testing.T) {
	r := newRig(ready(true))
	r.p.err = errors.New("нет соединения")
	called := false
	r.h.OnHandled = func(Cmd, Origin, string) { called = true }
	if r.h.Do(Next, Voice) {
		t.Fatal("невыполненная команда названа выполненной")
	}
	if called || r.plat.armedN() != 0 {
		t.Fatal("после неудачи были побочные действия")
	}
	// а повтор директивой потом должен пройти: молчаливый сбой не должен считаться «уже выполнено»
	r.p.err = nil
	if !r.h.Do(Next, Cloud) {
		t.Fatal("директива после неудачной фразы не выполнена")
	}
}

// Фраза выполнена перехватом, а облако (отмена диалога не успела) всё равно ответило директивой: второй раз не выполнять.
func TestCloudEchoIsIgnored(t *testing.T) {
	r := newRig(ready(true))
	if !r.h.Do(Next, Voice) {
		t.Fatal("фраза не взята")
	}
	r.advance(time.Second)
	if r.h.Do(Next, Cloud) || r.p.count() != 1 {
		t.Fatal("директива повторила уже выполненную команду: пропущено два трека вместо одного")
	}
	if !r.h.Do(Pause, Cloud) {
		t.Fatal("директива другого действия должна исполняться")
	}
	r.advance(cloudEcho + time.Second)
	if !r.h.Do(Pause, Cloud) {
		t.Fatal("директива через много секунд — уже новая команда")
	}
	// после жеста на колонке облако тоже может прислать «дальше»
	r2 := newRig(ready(true))
	r2.h.Do(Next, Speaker)
	if r2.h.Do(Next, Cloud) || r2.p.count() != 1 {
		t.Fatal("директива после жеста на колонке выполнена второй раз")
	}
}

func TestCloudResumeOnlyForPausedPlayer(t *testing.T) {
	r := newRig(ready(true))
	if r.h.Do(Resume, Cloud) {
		t.Fatal("player_continue взята у играющего плеера")
	}
	r.p.set(paused())
	if !r.h.Do(Resume, Cloud) {
		t.Fatal("player_continue при плеере на паузе не выполнена")
	}
}

// Платформа сама адресовала команду плееру Spotify (усыновлённая сессия): её исполняем при любом звуке, а то же нажатие, замеченное по логу
// платформы, второй раз не повторяем.
func TestPlatformCommandAndTheSpeakerEcho(t *testing.T) {
	r := newRig(ready(true))
	r.plat.setYandex(true) // платформа знает, кому адресует: её команда выполняется и при Яндексе в автомате
	if !r.h.Do(Pause, Virtual) {
		t.Fatal("команда платформы плееру не выполнена")
	}
	r.p.set(paused())
	r.h.SetOwner("spotify")
	r.plat.setYandex(false)
	r.advance(100 * time.Millisecond)
	if r.h.Do(Toggle, Speaker) || r.p.count() != 1 {
		t.Fatal("то же нажатие на колонке выполнено второй раз: пауза превратилась бы в продолжение")
	}
	if !r.h.Do(Toggle, Headphones) {
		t.Fatal("нажатие в наушниках проглочено защитой, которая нужна только колонке")
	}
	r.p.set(ready(true))
	if !r.h.Do(Next, Speaker) {
		t.Fatal("жест вперёд проглочен защитой переключателя")
	}
	r.advance(platformEcho + time.Second)
	if !r.h.Do(Toggle, Speaker) {
		t.Fatal("нажатие через пару секунд после команды платформы потеряно")
	}
}

// Что платформе остаётся сделать самой после нашей команды: на жест, «продолжи», «дальше» и «играй» она запросит у облака очередь Яндекса,
// и её запуск достаётся плееру. Пауза, наушники, директива облака и команда платформы очереди не затевают.
func TestCarrierIsArmedOnlyWhereThePlatformWillAnswer(t *testing.T) {
	type tc struct {
		cmd  Cmd
		from Origin
		st   State
		want int
	}
	for _, c := range []tc{
		{Next, Speaker, ready(true), 1}, {Prev, Speaker, ready(true), 1}, {Toggle, Speaker, paused(), 1}, {Toggle, Speaker, ready(true), 0},
		{Next, Voice, ready(true), 1}, {Resume, FastVoice, paused(), 1}, {Pause, Voice, ready(true), 0}, {Pause, FastVoice, ready(true), 0},
		{Next, Headphones, ready(true), 0}, {Toggle, Headphones, paused(), 0},
		{Next, Cloud, ready(true), 0}, {Pause, Virtual, ready(true), 0},
	} {
		r := newRig(c.st)
		r.h.SetOwner("spotify")
		if !r.h.Do(c.cmd, c.from) {
			t.Fatalf("%s %s: не выполнено", c.cmd, c.from)
		}
		if got := r.plat.armedN(); got != c.want {
			t.Errorf("%s %s: очередь Яндекса подготовлена %d раз, ждали %d", c.cmd, c.from, got, c.want)
		}
	}
}

func TestOwnsAndIdle(t *testing.T) {
	r := newRig(ready(true))
	if !r.h.Owns("spotify") || r.h.Owns("other") || r.h.Idle() {
		t.Fatal("играющий плеер должен быть хозяином звука, и тишины нет")
	}
	r.plat.setYandex(true)
	if r.h.Owns("spotify") || r.h.Idle() {
		t.Fatal("пока играет Яндекс, плеер не хозяин и тишины нет")
	}
	r2 := newRig(paused())
	if !r2.h.Owns("spotify") || !r2.h.Idle() {
		t.Fatal("плеер на паузе — хозяин звука, и ничего не играет")
	}
	r2.h.SetOwner(OwnerPlatform)
	if r2.h.Owns("spotify") {
		t.Fatal("после Яндекса плеер на паузе не хозяин")
	}
}

func TestOriginNamesAndPlatformReaction(t *testing.T) {
	for o, want := range map[Origin]bool{Speaker: true, Voice: true, FastVoice: true, Headphones: false, Cloud: false, Virtual: false, Arbiter: false} {
		if o.PlatformReacts() != want {
			t.Errorf("%s: PlatformReacts = %v, ждали %v", o, o.PlatformReacts(), want)
		}
		if o.String() == "" || o.String() == string(o) {
			t.Errorf("у источника %q нет русского названия", o)
		}
	}
	for c, want := range map[Cmd]string{Pause: "пауза", Resume: "продолжить", Toggle: "пауза/продолжить", Next: "следующий трек", Prev: "предыдущий трек"} {
		if c.String() != want {
			t.Errorf("%q: %q, ждали %q", string(c), c.String(), want)
		}
	}
}

func TestLen(t *testing.T) {
	var h Hub
	if h.Len() != 0 {
		t.Fatal("новый Hub не пуст")
	}
	h.Add("a", &fakePlayer{})
	if h.Len() != 1 {
		t.Fatalf("плееров %d, ждали 1", h.Len())
	}
}
