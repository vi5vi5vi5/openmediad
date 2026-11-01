package spotifysoloist

import (
	"context"
	"sync"
	"time"

	"openmediad/internal/libs/alsactl"
)

// Громкость: один владелец — платформа. Она хранит уровень 0–20 и переводит его в регулятор ALSA Master по своей таблице
// (libs/volmap); «Алиса, тише» и кнопки меняют именно этот уровень («тише» = уровень − 2). Если писать Master в обход неё,
// платформа продолжает считать, что уровень прежний, и первое же «тише» отсчитает от него: ползунок Spotify стоял на 30%, колонка
// «помнила» 100% — и после «тише» Spotify оказывался на 90%.
//
// Поэтому громкость Spotify выражается уровнем платформы (Spotify % = уровень × 5), а менять её платформе можно только её же
// способом — текстовой командой «громкость N» (как голосом; у неё N = 0…10, то есть уровень 2N, шаг 10%):
//
//	колонка → Spotify:  «тише», кнопки, приложение Яндекса меняют уровень → Master меняется → компонент ставит Soloist
//	                    громкость в процентах уровня, ползунок в приложении Spotify переезжает;
//	Spotify → колонка:  ползунок в приложении → через 300 мс покоя компонент просит платформу «громкость N» → она меняет уровень,
//	                    Master и экран, а ползунок Spotify подтягивается к тому, что получилось (шаг 10%).
//
// Шим громкость Soloist не применяет (OMD_SHIM_VOLUME=off): громкость звука — это Master. Приглушение под диалог Алисы устроено
// отдельно, в движке (класс music).

// masterControl — регулятор громкости колонки (элемент ALSA; amixer называет его «Master»).
const masterControl = "Master Playback Volume"

// masterVol — регулятор громкости колонки (alsactl.Watch); компонент только читает его и ждёт изменений.
type masterVol interface {
	Level() (cur, max int, ok bool)
	Changes() <-chan struct{}
	Close()
}

// openMaster открывает регулятор колонки (подменяется в тестах).
var openMaster = func() (masterVol, error) { return alsactl.WatchInteger(masterControl) }

// levelMap — таблица громкости платформы (libs/volmap).
type levelMap interface {
	Level(master int) int
	Levels() int
}

// soloistVol — то, что нужно от управления Soloist.
type soloistVol interface {
	state() playerState
	SetVolume(v int) error
}

// echoWindow — сколько после своей записи считать пришедшее изменение эхом этой записи.
const echoWindow = 1500 * time.Millisecond

// sliderIdle — сколько ползунок должен постоять, чтобы громкость ушла платформе (пока тянут, запросов нет).
const sliderIdle = 300 * time.Millisecond

type expectation struct {
	val   int
	until time.Time
}

func (e *expectation) set(v int, now time.Time) { e.val, e.until = v, now.Add(echoWindow) }

// consume — это эхо нашей записи? Тогда ожидание снимается.
func (e *expectation) consume(v int, now time.Time) bool {
	if e.until.IsZero() || now.After(e.until) || abs(v-e.val) > 1 {
		return false
	}
	*e = expectation{}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// volumeSync связывает громкость колонки (уровень платформы) и громкость Soloist.
type volumeSync struct {
	m     masterVol
	lv    levelMap
	ctl   soloistVol
	plat  func(n int) error // «громкость N» платформе, N = 0…10
	logf  func(string, ...any)
	now   func() time.Time
	idle  time.Duration
	steps int // сколько шагов у команды «громкость N» (10)

	events chan struct{} // что-то изменилось у Soloist (событие WebSocket)

	mu     sync.Mutex
	last   int // последняя громкость колонки, %
	pushes struct{ toSoloist, toPlatform int }
	lastN  int // что просили у платформы последним
}

func newVolumeSync(m masterVol, lv levelMap, ctl soloistVol, plat func(n int) error, logf func(string, ...any)) *volumeSync {
	return &volumeSync{m: m, lv: lv, ctl: ctl, plat: plat, logf: logf, now: time.Now, idle: sliderIdle, steps: 10,
		events: make(chan struct{}, 1), last: -1, lastN: -1}
}

// poke сообщает, что у Soloist что-то изменилось (вызывается из разбора событий; не блокируется).
func (v *volumeSync) poke() {
	select {
	case v.events <- struct{}{}:
	default:
	}
}

// percent — громкость колонки в процентах уровня платформы (-1, если регулятор закрыт).
func (v *volumeSync) percent() int {
	cur, max, ok := v.m.Level()
	if !ok || max <= 0 || v.lv.Levels() <= 0 {
		return -1
	}
	return (v.lv.Level(cur)*100 + v.lv.Levels()/2) / v.lv.Levels()
}

type volStatus struct {
	MasterPct  int `json:"speaker_percent"`
	ToSoloist  int `json:"pushed_to_soloist"`
	ToPlatform int `json:"requested_from_platform"`
	LastN      int `json:"last_requested_n"`
	SoloistPct int `json:"soloist_percent"`
}

func (v *volumeSync) status() volStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	return volStatus{MasterPct: v.last, ToSoloist: v.pushes.toSoloist, ToPlatform: v.pushes.toPlatform, LastN: v.lastN, SoloistPct: v.ctl.state().Volume}
}

// run следит за громкостью колонки и ползунком Soloist, пока не отменён ctx; закрывает регулятор колонки.
//
// Громкость Soloist читается из его событий и считается «ползунком», только если она именно изменилась с прошлого сообщения и не
// похожа на эхо нашей же записи. Если Soloist наши записи не повторяет (команда отвергнута, событие потерялось), колонка из-за
// устаревших данных не двигается.
func (v *volumeSync) run(ctx context.Context) {
	defer v.m.Close()
	var echoS expectation // что мы сами только что поставили Soloist
	seen := -1            // последняя громкость Soloist из его сообщений; -1 — после входа ещё не сообщал
	pending := -1         // громкость ползунка (%), которую ещё не отдали платформе
	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	v.mu.Lock()
	v.last = v.percent()
	v.mu.Unlock()
	pushToSoloist := func(p int) {
		if err := v.ctl.SetVolume(p); err != nil {
			v.logf("громкость колонки %d%%: передать Spotify не вышло: %v", p, err)
			return
		}
		echoS.set(p, v.now())
		v.mu.Lock()
		v.pushes.toSoloist++
		v.mu.Unlock()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-v.m.Changes():
			p := v.percent()
			v.mu.Lock()
			same := p < 0 || p == v.last
			v.last = p
			v.mu.Unlock()
			if same {
				continue
			}
			// «Алиса, тише», кнопки, приложение Яндекса (или наш же запрос «громкость N»): ползунок Spotify переезжает к колонке
			if st := v.ctl.state(); st.Connected && st.LoggedIn && st.Volume != p {
				pushToSoloist(p)
			}
		case <-v.events:
			st := v.ctl.state()
			if !st.Connected || !st.LoggedIn {
				seen = -1
				continue
			}
			if st.Volume < 0 {
				continue
			}
			if seen < 0 {
				// первое сообщение после входа: Spotify подстраивается под колонку, а не наоборот
				seen = st.Volume
				if p := v.percent(); p >= 0 && p != st.Volume {
					pushToSoloist(p)
				}
				continue
			}
			if st.Volume == seen {
				continue
			}
			seen = st.Volume
			if echoS.consume(st.Volume, v.now()) {
				continue
			}
			// ползунок в приложении Spotify: ждём, пока он остановится, и просим платформу
			pending = st.Volume
			if timer == nil {
				timer = time.NewTimer(v.idle)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(v.idle)
			}
			fire = timer.C
		case <-fire:
			fire = nil
			p := pending
			pending = -1
			if p < 0 {
				continue
			}
			n := (p*v.steps + 50) / 100
			if n > v.steps {
				n = v.steps
			}
			if v.percent() == n*100/v.steps || v.plat == nil {
				continue // уровень платформы уже такой
			}
			if err := v.plat(n); err != nil {
				v.logf("громкость Spotify %d%%: попросить платформу «громкость %d» не вышло: %v", p, n, err)
				continue
			}
			v.mu.Lock()
			v.pushes.toPlatform++
			v.lastN = n
			v.mu.Unlock()
		}
	}
}
