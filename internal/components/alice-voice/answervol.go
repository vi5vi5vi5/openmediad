package alicevoice

import (
	"strings"
	"sync"
	"time"
)

// Громкость ответов. Прошивка колонки на время каждого ответа Алисы сама меняет громкость (функция SmartVolume: по шуму в комнате
// поднимает тихую громкость и опускает громкую) и возвращает её, когда ответ кончился. Решение принимается заново на каждый ответ,
// поэтому соседние ответы одного диалога могут прозвучать по-разному (проверено по журналу платформы 11.11.2026: «Estimated ambient
// noise volume … current volume in percent: 20, matched volume: 40», затем «Restore pre-request volume»).
//
// С настройкой answer_volume = "hold" компонент держит громкость колонки на уровне до ответа: перемену, которую платформа сделала
// сама, сразу возвращает. Отличить её от просьбы человека можно по ответу облака: платформа меняет громкость в тот же миг, когда
// ответ пришёл (разница до 10 мс по журналам), и в ответе нет директив громкости. «Алиса, громче», кнопки колонки и колесо наушников
// выглядят иначе (в ответе есть директива громкости или ответа рядом нет совсем), и такая перемена принимается как новая громкость.
//
// Удерживается только регулятор колонки (ALSA Master). Громкость Bluetooth-наушников платформа ставит им сама по AVRCP, мимо него.

// Значения настройки answer_volume.
const (
	AnswerVolumePlatform = "platform" // как решит платформа (штатное поведение)
	AnswerVolumeHold     = "hold"     // держать громкость, которая была до ответа
)

const (
	// replyWindow — сколько после ответа облака перемена громкости считается сделанной платформой для этого ответа.
	replyWindow = 400 * time.Millisecond
	// settleWindow — сколько перемена громкости посреди диалога ждёт ответа облака, прежде чем считаться просьбой человека.
	settleWindow = 150 * time.Millisecond
	// holdAfterRevert — сколько после возврата громкости возвращаются и следующие перемены (платформа может ставить её в несколько шагов).
	holdAfterRevert = 300 * time.Millisecond
)

// masterCtl — регулятор громкости колонки (alsactl.Watch; в тестах — подделка).
type masterCtl interface {
	Level() (cur, max int, ok bool)
	Changes() <-chan struct{}
	Set(v int) error
	Close()
}

type keeperEvent struct {
	focus  bool // это смена фокуса (иначе — ответ облака)
	on     bool
	manual bool // в ответе есть директива громкости
}

// answerKeeper держит громкость колонки на время ответов Алисы.
type answerKeeper struct {
	m    masterCtl
	logf func(string, ...any)
	now  func() time.Time

	events chan keeperEvent
	done   chan struct{}
	once   sync.Once
	exited chan struct{}

	mu       sync.Mutex
	base     int // громкость, которую выбрал человек
	reverted int // сколько раз возвращали громкость
	accepted int // сколько перемен посреди диалога приняли как просьбу человека
}

func newAnswerKeeper(m masterCtl, logf func(string, ...any)) *answerKeeper {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	k := &answerKeeper{m: m, logf: logf, now: time.Now, events: make(chan keeperEvent, 16), done: make(chan struct{}), exited: make(chan struct{})}
	k.base, _, _ = m.Level()
	go k.run()
	return k
}

// volumeDirective — директива ответа про громкость: человек попросил её поменять («громче», «громкость пять», «без звука»).
func volumeDirective(name string) bool {
	return strings.HasPrefix(name, "sound_") || strings.HasPrefix(name, "volume_")
}

// OnFocus — диалог начался (true) или кончился.
func (k *answerKeeper) OnFocus(on bool) { k.send(keeperEvent{focus: true, on: on}) }

// OnReply — пришёл ответ облака с такими директивами.
func (k *answerKeeper) OnReply(directives []string) {
	e := keeperEvent{}
	for _, d := range directives {
		if volumeDirective(d) {
			e.manual = true
		}
	}
	k.send(e)
}

func (k *answerKeeper) send(e keeperEvent) {
	select {
	case k.events <- e:
	case <-k.done:
	default: // очередь переполнена: лучше пропустить событие, чем задержать разбор шины
	}
}

// Close останавливает слежение и закрывает регулятор.
func (k *answerKeeper) Close() {
	k.once.Do(func() { close(k.done) })
	<-k.exited
}

type keeperStatus struct {
	Base     int `json:"base_master"`
	Reverted int `json:"reverted"`
	Accepted int `json:"accepted_in_dialog"`
}

func (k *answerKeeper) status() keeperStatus {
	k.mu.Lock()
	defer k.mu.Unlock()
	return keeperStatus{Base: k.base, Reverted: k.reverted, Accepted: k.accepted}
}

func (k *answerKeeper) setBase(v int) {
	k.mu.Lock()
	k.base = v
	k.mu.Unlock()
}

func (k *answerKeeper) getBase() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.base
}

func (k *answerKeeper) run() {
	defer close(k.exited)
	defer k.m.Close()
	var (
		dialog     bool
		smartUntil time.Time // до этого момента перемена громкости — дело платформы
		pending    = -1      // перемена посреди диалога, про которую ещё не ясно, чья она
		pendingAt  time.Time
		fire       <-chan time.Time
		logged     bool // про этот диалог в журнал уже написано
	)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	arm := func(d time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
		fire = timer.C
	}
	accept := func(why string) {
		if pending < 0 {
			return
		}
		k.setBase(pending)
		k.mu.Lock()
		k.accepted++
		k.mu.Unlock()
		k.logf("громкость колонки изменена во время диалога (%s): принята как новая", why)
		pending, fire = -1, nil
	}
	revert := func(got int) {
		base := k.getBase()
		pending, fire = -1, nil
		smartUntil = k.now().Add(holdAfterRevert)
		if err := k.m.Set(base); err != nil {
			k.logf("громкость ответа: вернуть громкость колонки не вышло: %v", err)
			return
		}
		k.mu.Lock()
		k.reverted++
		k.mu.Unlock()
		if !logged {
			logged = true
			k.logf("громкость ответа удержана: платформа поставила на время ответа %d, возвращено %d (answer_volume = \"hold\")", got, base)
		}
	}
	for {
		select {
		case <-k.done:
			return
		case e := <-k.events:
			switch {
			case e.focus:
				if !e.on {
					accept("ответа облака рядом не было")
					smartUntil = time.Time{}
				}
				dialog, logged = e.on, false
			case e.manual:
				smartUntil = time.Time{}
				accept("об этом попросили голосом")
			case pending >= 0 && k.now().Sub(pendingAt) <= settleWindow:
				revert(pending)
			default:
				smartUntil = k.now().Add(replyWindow)
			}
		case <-k.m.Changes():
			cur, _, ok := k.m.Level()
			switch {
			case !ok:
			case cur == k.getBase():
				pending, fire = -1, nil // вернулась сама (или это наша же запись)
			case !dialog:
				k.setBase(cur)
			case k.now().Before(smartUntil):
				revert(cur)
			default:
				pending, pendingAt = cur, k.now()
				arm(settleWindow)
			}
		case <-fire:
			fire = nil
			accept("ответа облака рядом не было")
		}
	}
}
