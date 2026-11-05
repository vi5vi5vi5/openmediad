package control

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// OwnerPlatform — «продолжи» после паузы достаётся музыке платформы (Яндекс.Музыка): она играла позже плееров движка.
	OwnerPlatform = "platform-music"

	// carrierWindow — сколько после команды запуск музыки Яндекса платформой считается её следствием: audio_play приходит через 0,1–0,7 с.
	carrierWindow = 3 * time.Second
	// platformEcho — сколько после команды платформы своему плееру то же нажатие кнопки колонки, замеченное по логу, считается повтором:
	// иначе пауза тут же сменилась бы продолжением.
	platformEcho = 1500 * time.Millisecond
	// cloudEcho — сколько после выполненной команды та же директива облака считается её повтором: отмена диалога не всегда успевает,
	// облако отвечает на уже выполненную фразу.
	cloudEcho = 6 * time.Second
	// arbitrateEvery — как часто смотреть, кто заиграл и что случилось с плеерами: два потока не звучат вместе дольше доли секунды, а анимация паузы из приложения не запаздывает.
	arbitrateEvery = 100 * time.Millisecond
)

type member struct {
	name string
	p    Player
}

// Hub собирает плееры, решает, кому достаётся команда, и следит, чтобы Spotify и Яндекс не играли вместе.
type Hub struct {
	Platform Platform // музыка Яндекса и автомат mediad (nil — только плееры движка)
	// OnHandled (необязательный) вызывается, когда плеер выполнил команду: для журнала.
	OnHandled func(c Cmd, from Origin, player string)
	// Logf (необязательный) — журнал уступок звука.
	Logf func(string, ...any)
	// Now — часы (по умолчанию time.Now).
	Now func() time.Time

	mu         sync.Mutex
	players    []member
	owner      string    // кто последним начал играть: имя плеера или OwnerPlatform
	platformAt time.Time // когда платформа последний раз сама адресовала команду плееру
	lastCmd    Cmd       // что выполнено последним (для защиты от повтора директивой облака)
	lastAt     time.Time

	subs   map[int]func(Event)
	nextID int
	seen   map[string]*seenPlayer // что видели у каждого плеера на прошлом шаге Run
}

// seenPlayer — память о плеере между шагами Run: по ней замечают паузу и перемотку, сделанные не через Hub.
type seenPlayer struct {
	stable    State // последнее устойчивое состояние (играет или на паузе): между ними плеер ненадолго буферизует
	hasStable bool
	// stableTrack и stableNear — трек и «доигрывается ли он» в последнем устойчивом состоянии (играет или на паузе)
	stableTrack string
	stableNear  bool
	tracks      []string          // последние несколько разных треков: по ним отличают «назад» от «дальше»
	emitted     map[Cmd]time.Time // когда объявляли событие этого вида (по команде через Hub, уступке звука или самому изменению)
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) logf(f string, a ...any) {
	if h.Logf != nil {
		h.Logf(f, a...)
	}
}

// Add добавляет плеер. Спрашивают плееры в том порядке, в каком их добавили.
func (h *Hub) Add(name string, p Player) {
	h.mu.Lock()
	h.players = append(h.players, member{name, p})
	h.mu.Unlock()
}

// Len — сколько плееров подключено.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.players)
}

func (h *Hub) snapshot() []member {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]member(nil), h.players...)
}

// Owner — кто последним начал играть: имя плеера, OwnerPlatform или пусто (ещё никто).
func (h *Hub) Owner() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owner
}

// SetOwner отмечает, кто последним начал играть (Run делает это сам; нужен тестам и компонентам, узнавшим о старте раньше).
func (h *Hub) SetOwner(name string) {
	h.mu.Lock()
	h.owner = name
	h.mu.Unlock()
}

// situation — что звучит прямо сейчас, глазами тех плееров, которые слушаются этого источника.
type situation struct {
	yandex    bool    // играет музыка Яндекса
	owner     string  // кто последним начал играть
	playing   *member // плеер, у которого играет музыка
	resumable *member // плеер, стоящий на паузе, на котором сессия
	first     *member // первый готовый плеер
}

func (h *Hub) situation(from Origin) situation {
	s := situation{yandex: h.Platform != nil && h.Platform.Playing(), owner: h.Owner()}
	list := h.snapshot()
	for i := range list {
		m := &list[i]
		if from != "" && !m.p.Accepts(from) {
			continue
		}
		st := m.p.State()
		if !st.Ready {
			continue
		}
		if s.first == nil {
			s.first = m
		}
		switch {
		case st.Playing && s.playing == nil:
			s.playing = m
		case st.Resumable && !st.Playing && s.resumable == nil:
			s.resumable = m
		}
	}
	return s
}

// active — музыка идёт у плеера движка, а Яндекс молчит: команды плееру его.
func (s situation) active() *member {
	if s.playing != nil && !s.yandex {
		return s.playing
	}
	return nil
}

// paused — ничего не играет, а на паузе стоит плеер движка, и Яндекс не играл позже: «продолжи» и «дальше» его.
func (s situation) paused() *member {
	if s.playing == nil && !s.yandex && s.resumable != nil && s.owner != OwnerPlatform {
		return s.resumable
	}
	return nil
}

// Owns говорит, что плеер name сейчас хозяин звука: у него идёт музыка или стоит пауза, и Яндекс не играл позже. Нужно самому плееру для его
// команд, которые не про воспроизведение (лайк).
func (h *Hub) Owns(name string) bool {
	s := h.situation("")
	for _, m := range []*member{s.active(), s.paused()} {
		if m != nil && m.name == name {
			return true
		}
	}
	return false
}

// Idle — ничего не играет: ни плеер движка, ни Яндекс («включи музыку» — просьба заиграть что-то).
func (h *Hub) Idle() bool {
	s := h.situation("")
	return s.playing == nil && !s.yandex
}

// Do отдаёт команду плеерам; true — её выполнил плеер движка. false — никто не взял (играет Яндекс.Музыка или ничего, команда не ваша, нет связи):
// источник поступает по-старому.
func (h *Hub) Do(c Cmd, from Origin) bool {
	if h == nil {
		return false
	}
	now := h.now()
	h.mu.Lock()
	switch {
	case from == Virtual:
		h.platformAt = now
	case from == Speaker && c == Toggle && now.Sub(h.platformAt) < platformEcho:
		h.mu.Unlock()
		return false // платформа уже поставила на паузу или сняла сама, как со своим плеером
	}
	h.mu.Unlock()

	s := h.situation(from)
	var target *member
	act := c
	switch {
	case from == Virtual:
		// платформа адресовала команду именно плееру движка, чей id она знает: чьим бы ни был звук, исполняем
		target = s.first
		if c == Toggle {
			act = Resume
			if s.playing != nil {
				act = Pause
			}
		}
	case c == Pause:
		target = s.active()
	case c == Resume:
		target = s.paused()
	case c == Next || c == Prev:
		if target = s.active(); target == nil {
			target = s.paused()
		}
	case c == Toggle:
		if target = s.active(); target != nil {
			act = Pause
		} else if target = s.paused(); target != nil {
			act = Resume
		}
	}
	if target == nil {
		return false
	}
	if from == Cloud {
		h.mu.Lock()
		echo := h.lastCmd == act && now.Sub(h.lastAt) < cloudEcho
		h.mu.Unlock()
		if echo {
			return false
		}
	}
	if err := target.p.Do(act, from); err != nil {
		return false
	}
	if from != Virtual {
		h.mu.Lock()
		h.lastCmd, h.lastAt = act, now
		h.mu.Unlock()
	}
	h.emit(Event{Cmd: act, From: from, Player: target.name})
	if act != Pause && from.PlatformReacts() && h.Platform != nil {
		// платформа запросит у облака очередь Яндекса: этот запуск достанется плееру движка
		h.Platform.ArmCarrier(carrierWindow)
	}
	if h.OnHandled != nil {
		h.OnHandled(act, from, target.name)
	}
	return true
}

// Subscribe подписывает f на события плееров (Event); вызывается на каждое событие по очереди, поэтому f должна быть быстрой. Возвращает отписку.
func (h *Hub) Subscribe(f func(Event)) (cancel func()) {
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[int]func(Event){}
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = f
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

// emit объявляет событие подписчикам и запоминает его: то же самое, замеченное потом по состоянию плеера, повторно не объявляется.
func (h *Hub) emit(e Event) {
	h.note(e.Player, e.Cmd)
	h.mu.Lock()
	subs := make([]func(Event), 0, len(h.subs))
	for _, f := range h.subs {
		subs = append(subs, f)
	}
	h.mu.Unlock()
	for _, f := range subs {
		f(e)
	}
}

// note запоминает, что событие этого вида у плеера уже было (объявлено или причина известна: его уступка звука).
func (h *Hub) note(player string, c Cmd) {
	now := h.now()
	h.mu.Lock()
	h.seenOf(player).emitted[c] = now
	h.mu.Unlock()
}

// seenOf — память о плеере (mu занят).
func (h *Hub) seenOf(name string) *seenPlayer {
	if h.seen == nil {
		h.seen = map[string]*seenPlayer{}
	}
	s := h.seen[name]
	if s == nil {
		s = &seenPlayer{emitted: map[Cmd]time.Time{}}
		h.seen[name] = s
	}
	return s
}

// eventEcho — сколько после объявленного события то же изменение состояния плеера считается его следствием: Soloist присылает новый статус и
// трек не сразу, а через десятые доли секунды.
func eventEcho(c Cmd) time.Duration {
	if c == Next || c == Prev {
		return 4 * time.Second
	}
	return 2 * time.Second
}

// derive — какое событие значит смена состояния плеера с prev на cur (false — никакое). Пауза и «продолжить» — это переход между «играет» и
// «на паузе»; окончание сессии, обрыв связи и промежуточные состояния (буферизация) ими не считаются.
func derive(prev, cur State) (Cmd, bool) {
	switch {
	case prev.Playing && !cur.Playing && cur.Paused:
		return Pause, true
	case !prev.Playing && prev.Paused && cur.Playing:
		return Resume, true
	}
	return "", false
}

// detect сравнивает состояние каждого плеера с прошлым шагом и объявляет то, что произошло само: паузу и перемотку из приложения.
// Перемотка — это смена трека, когда прошлый трек не доигрывался (иначе просто началась следующая песня). Треки сравниваются только в
// устойчивых состояниях (играет или на паузе): в промежутке между треками Soloist ненадолго буферизует, и смена видна раньше, чем плеер заиграл.
func (h *Hub) detect(list []member, states map[string]State) {
	now := h.now()
	var events []Event
	h.mu.Lock()
	for _, m := range list {
		s, cur := h.seenOf(m.name), states[m.name]
		steady := cur.Playing || cur.Paused
		var c Cmd
		var ok bool
		if steady && s.hasStable { // сравниваем с прошлым устойчивым: между «на паузе» и «играет» бывает буферизация
			c, ok = derive(s.stable, cur)
		}
		if steady {
			s.stable, s.hasStable = cur, true
		}
		if steady && cur.Track != "" {
			if s.stableTrack != "" && cur.Track != s.stableTrack {
				// «назад»: вернулись к треку, который играл перед прошлым
				back := len(s.tracks) >= 2 && s.tracks[len(s.tracks)-2] == cur.Track
				if !ok && !s.stableNear {
					c, ok = Next, true
					if back {
						c = Prev
					}
				}
			}
			if cur.Track != s.stableTrack {
				s.tracks = append(s.tracks, cur.Track)
				if len(s.tracks) > 4 {
					s.tracks = s.tracks[len(s.tracks)-4:]
				}
			}
			s.stableTrack, s.stableNear = cur.Track, cur.NearEnd
		}
		if !ok || recent(s, c, now) {
			continue
		}
		events = append(events, Event{Cmd: c, From: App, Player: m.name})
	}
	h.mu.Unlock()
	for _, e := range events {
		h.emit(e)
	}
}

// recent — не было ли недавно события этого вида, которое и объясняет замеченную перемену.
func recent(s *seenPlayer, c Cmd, now time.Time) bool {
	kinds := []Cmd{c}
	if c == Next || c == Prev {
		kinds = []Cmd{Next, Prev}
	}
	for _, k := range kinds {
		if at, ok := s.emitted[k]; ok && now.Sub(at) < eventEcho(c) {
			return true
		}
	}
	return false
}

// Run следит за плеерами раз в 100 мс. Уступает звук: заиграл плеер движка — музыка Яндекса на паузе, заиграл Яндекс — плеер на паузе.
// Побеждает тот, кто заиграл позже (при одновременном старте — плеер движка). Запоминает, кто играл последним: ему достанется «продолжи» после
// паузы. И объявляет события, которые случились без Hub: паузу и перемотку из приложения. Работает, пока не отменят ctx.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(arbitrateEvery)
	defer t.Stop()
	prev := map[string]bool{}
	var prevY bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		prevY = h.arbitrate(prev, prevY)
	}
}

// arbitrate — один шаг Run; возвращает, играл ли Яндекс на этом шаге (для следующего).
func (h *Hub) arbitrate(prev map[string]bool, prevY bool) bool {
	y := h.Platform != nil && h.Platform.Playing()
	list := h.snapshot()
	states := map[string]State{}
	sounding := map[string]bool{}
	var rose []member
	for _, m := range list {
		st := m.p.State()
		states[m.name] = st
		sounding[m.name] = st.Sounding
		if st.Sounding && !prev[m.name] {
			rose = append(rose, m)
		}
	}
	h.detect(list, states)
	if h.Platform == nil {
		for _, m := range list {
			prev[m.name] = sounding[m.name]
		}
		if len(rose) > 0 {
			h.SetOwner(rose[len(rose)-1].name)
		}
		return y
	}
	switch {
	case len(rose) > 0:
		h.SetOwner(rose[len(rose)-1].name)
	case y && !prevY:
		h.SetOwner(OwnerPlatform)
	}
	switch {
	case len(rose) > 0:
		// Яндекс играл — остановить; на паузе у платформы остался его плеер — отдать место плееру движка (видно на дисплее)
		if n := h.Platform.TakeOver(); n > 0 {
			for _, m := range rose {
				if yl, ok := m.p.(Yielder); ok {
					yl.Yielded()
				}
				h.logf("заиграл %s: музыка Яндекса (плееров: %d) остановлена", m.name, n)
			}
		}
		y = false
		// и другие плееры движка, звучавшие до него, уступают
		for _, m := range list {
			if sounding[m.name] && prev[m.name] {
				h.pause(m, fmt.Sprintf("заиграл %s", rose[len(rose)-1].name))
			}
		}
	case y && !prevY:
		for _, m := range list {
			if sounding[m.name] {
				h.pause(m, "заиграл Яндекс")
			}
		}
	}
	for _, m := range list {
		prev[m.name] = sounding[m.name]
	}
	return y
}

// pause ставит плеер на паузу из-за того, что заиграл другой источник. Пауза по такой причине не событие для подсветки: сразу следом заиграл
// другой источник со своей подсветкой.
func (h *Hub) pause(m member, why string) {
	h.note(m.name, Pause) // чтобы пауза, замеченная следом по состоянию, не объявилась как «из приложения»
	if err := m.p.Do(Pause, Arbiter); err != nil {
		h.logf("%s, но %s поставить на паузу не вышло: %v", why, m.name, err)
		return
	}
	if yl, ok := m.p.(Yielder); ok {
		yl.Yielded()
	}
	h.logf("%s: %s поставлен на паузу", why, m.name)
}
