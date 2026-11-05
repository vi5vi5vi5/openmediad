package mediad

// Виртуальный плеер: источник, который играет мимо автомата (сессия Spotify из приложения). Платформа узнаёт о плеерах музыки только
// из событий mediad, а облако отвечает на «пауза», «играй», «дальше» по тому, что платформа считает последним плеером; слой
// спектра на дисплее тоже включается по состоянию плеера. Поэтому автомат сообщает платформе о таком источнике теми же событиями
// AudioClientEvent, что и о своём плеере музыки, а команды платформы этому плееру (пауза, освобождение) отдаёт источнику.
// Звук виртуальный плеер не ведёт: его ведёт сам источник, а автомат только пишет ему биографию.
//
// Платформа принимает события плеера, которого сама не запускала, только пока у неё нет своей активной сессии музыки (после
// audio_play сессия есть, и чужие плееры её состояние не меняют: дисплей показывает спектр только «её» плееру). Поэтому виртуальный плеер
// умеет «занять» плеер платформы: подставить своё состояние под её player id, не трогая звука. Это нужно в двух случаях:
//   - Spotify заиграл, пока играл Яндекс (TakeOver): плеер Яндекса замолкает без события, а платформа дальше видит под его id Spotify;
//   - быстрая команда («дальше», «назад», «играй» без «Алиса») заставила платформу локально запустить audio_play очереди Яндекса, а
//     Spotify уже выполнил её сам (ArmCarrier): запуск не играется, платформа получает под его id события Spotify.

import (
	"time"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
)

// Virtual — состояние источника, который играет мимо автомата.
type Virtual struct {
	Track   string    // идентификатор трека (у Spotify spotify:track:…); смена идентификатора — новый трек
	Title   string    // название
	Artist  string    // исполнитель
	DurMs   uint64    // длительность, мс (0 — неизвестна)
	PosMs   uint64    // позиция, мс, на момент PosAt
	PosAt   time.Time // когда известна позиция (нулевое — сейчас)
	Playing bool      // играет, иначе на паузе
}

// seekJump — на сколько мс позиция источника должна разойтись с расчётной, чтобы считать это перемоткой.
const seekJump = 3000

type vplayer struct {
	meta     Meta
	track    string
	title    string
	artist   string
	adopted  bool  // под id плеера платформы (а не под собственным)
	st       state // stPlaying, stPaused или stEnded
	vsid     string
	mark     string
	startMs  uint64
	stopMs   uint64
	durMs    uint64
	basePos  uint64
	baseAt   time.Time
	seenProg bool
	t        timers
}

// pos — позиция виртуального плеера на момент now.
func (v *vplayer) pos(now time.Time) uint64 {
	p := v.basePos
	if v.st == stPlaying && now.After(v.baseAt) {
		p += uint64(now.Sub(v.baseAt).Milliseconds())
	}
	if v.durMs > 0 && p > v.durMs {
		p = v.durMs
	}
	return p
}

// OnVirtualCommand задаёт, кому платформа адресует команды виртуального плеера: имя команды pause, release или play (всё остальное — other).
func (m *Machine) OnVirtualCommand(f func(cmd string)) {
	m.mu.Lock()
	m.vcmd = f
	m.mu.Unlock()
}

// SetVirtual сообщает автомату состояние источника; nil — источник остановился. Повторный вызов с тем же состоянием ничего не отправляет.
func (m *Machine) SetVirtual(v *Virtual) {
	m.mu.Lock()
	m.init()
	now := m.Now()
	var evs [][]byte
	vp := m.vp
	start := false // у нового или возобновлённого плеера нужны таймеры прогресса
	switch {
	case v == nil:
		if vp == nil || vp.st == stEnded {
			break
		}
		if vp.adopted { // плеер платформы: ENDED запустил бы у облака «дальше» по очереди Яндекса, поэтому он только встаёт на паузу
			if vp.st == stPlaying {
				evs = append(evs, m.vpause(vp, now))
			}
			break
		}
		evs = append(evs, m.vend(vp, now))
		m.vp = nil
	case vp == nil || vp.st == stEnded || vp.track != v.Track && !vp.adopted:
		if vp != nil && vp.st != stEnded {
			evs = append(evs, m.vend(vp, now))
		}
		vp = m.vstart(v, now)
		m.vp = vp
		if m.carrier != nil { // место для Spotify уже занято у платформы
			m.vbind(vp, *m.carrier)
			m.carrier = nil
		}
		pos := vp.basePos
		evs = append(evs, m.vevent(vp, 1, 1, pos)) // STARTING
		if v.Playing {
			vp.st = stPlaying
			evs = append(evs, m.vevent(vp, 1, 3, pos), m.vevent(vp, 2, 3, pos))
			start = true
		} else {
			vp.st = stPaused
			vp.stopMs = uint64(now.UnixMilli())
			evs = append(evs, m.vevent(vp, 1, 4, pos))
			start = true // таймер, который закроет плеер, простоявший на паузе
		}
		m.note("virtual", vp.meta.Player.ID, "новый плеер: "+v.Title)
	default:
		if v.DurMs > 0 {
			vp.durMs = v.DurMs
		}
		cur := vp.pos(now)
		want := v.PosMs
		if v.Playing && !v.PosAt.IsZero() && now.After(v.PosAt) {
			want += uint64(now.Sub(v.PosAt).Milliseconds())
		}
		if vp.track != v.Track { // сессия платформы продолжается, играет другой трек: меняем описание на месте
			vp.track, vp.title, vp.artist = v.Track, v.Title, v.Artist
			vp.meta.Desc = retitle(vp.meta.Desc, v.Title, v.Artist)
			vp.basePos, vp.baseAt = want, now
			vp.seenProg = false
			vp.t.stop()
			if v.Playing {
				vp.st, vp.stopMs = stPlaying, 0
				evs = append(evs, m.vevent(vp, 1, 3, want), m.vevent(vp, 2, 3, want))
			} else {
				vp.st, vp.stopMs = stPaused, uint64(now.UnixMilli())
				evs = append(evs, m.vevent(vp, 1, 4, want))
			}
			start = true
			m.note("virtual", vp.meta.Player.ID, "другой трек в той же сессии: "+v.Title)
			break
		}
		vp.title, vp.artist = v.Title, v.Artist
		switch {
		case v.Playing && vp.st != stPlaying: // возобновили
			vp.t.stop()
			vp.st = stPlaying
			vp.stopMs = 0
			vp.basePos, vp.baseAt = want, now
			evs = append(evs, m.vevent(vp, 1, 3, want), m.vevent(vp, 2, 3, want))
			start = true
		case !v.Playing && vp.st == stPlaying: // поставили на паузу
			vp.basePos = cur
			evs = append(evs, m.vpause(vp, now))
			start = true
		case want+seekJump < cur || cur+seekJump < want: // перемотали
			vp.basePos, vp.baseAt = want, now
			if vp.st == stPlaying {
				vp.seenProg = false
				evs = append(evs, m.vevent(vp, 2, 3, want))
			}
		}
	}
	m.mu.Unlock()
	for _, e := range evs {
		m.Emit(e)
	}
	if start && vp != nil {
		m.vtimers(vp)
	}
}

// vstart создаёт виртуальный плеер. Вызывать под m.mu.
func (m *Machine) vstart(v *Virtual, now time.Time) *vplayer {
	desc := pb.AppendString(nil, 1, v.Track)
	desc = pb.AppendBytes(desc, 8, pb.AppendString(nil, 2, "music"))
	desc = retitle(desc, v.Title, v.Artist)
	vp := &vplayer{
		meta:    Meta{Player: msg.Player{Kind: msg.PlayerAudio, ID: newUUID(), Stream: v.Track}, Desc: desc},
		track:   v.Track,
		title:   v.Title,
		artist:  v.Artist,
		vsid:    newVsid(now),
		mark:    randString(8),
		startMs: uint64(now.UnixMilli()),
		durMs:   v.DurMs,
		basePos: v.PosMs,
		baseAt:  now,
	}
	if v.Playing && !v.PosAt.IsZero() && now.After(v.PosAt) {
		vp.basePos += uint64(now.Sub(v.PosAt).Milliseconds())
	}
	return vp
}

// vbind переводит виртуальный плеер под player id и описание плеера платформы. Вызывать под m.mu.
func (m *Machine) vbind(vp *vplayer, meta Meta) {
	vp.meta = Meta{Player: meta.Player, Ref: meta.Ref, Reused: meta.Reused, Desc: retitle(meta.Desc, vp.title, vp.artist)}
	vp.adopted = true
	vp.seenProg = false
}

// vpause ставит играющий виртуальный плеер на паузу и возвращает событие. Вызывать под m.mu.
func (m *Machine) vpause(vp *vplayer, now time.Time) []byte {
	pos := vp.pos(now)
	vp.t.stop()
	vp.basePos = pos
	vp.st = stPaused
	vp.stopMs = uint64(now.UnixMilli())
	return m.vevent(vp, 1, 4, pos)
}

// vend заканчивает плеер: ENDED с позицией. Вызывать под m.mu.
func (m *Machine) vend(vp *vplayer, now time.Time) []byte {
	pos := vp.pos(now)
	vp.t.stop()
	vp.st = stEnded
	vp.stopMs = uint64(now.UnixMilli())
	m.note("virtual", vp.meta.Player.ID, "плеер закончен")
	return m.vevent(vp, 1, 5, pos)
}

// vevent собирает событие виртуального плеера. Вызывать под m.mu.
func (m *Machine) vevent(vp *vplayer, kind, st, posMs uint64) []byte {
	s := Snap{
		Kind: msg.EventKind(kind), State: msg.PlayState(st),
		PosMs: posMs, Idx: posMs / 1000, UUID: newUUID(),
		Vsid: vp.vsid, Start: vp.startMs, Stop: vp.stopMs,
	}
	if vp.durMs > 0 {
		s.HasDur, s.DurS = true, (vp.durMs+500)/1000
		s.Mono = m.Mono()
	}
	switch kind {
	case 2:
		s.HasT9, s.T9 = true, vp.seenProg
		vp.seenProg = true
	case 6:
		s.HasT17, s.T17 = true, !m.ducked
	}
	s.Diag = Diag(vp.mark, vp.vsid, 0, 0, st == 5, posMs/1000)
	return vp.meta.Event(s)
}

// vtimers запускает таймеры: у играющего прогресс (раз в секунду) и первый flag, как у плеера музыки; у стоящего на паузе адоптированного —
// закрытие через pauseCache (как у настоящего mediad).
func (m *Machine) vtimers(vp *vplayer) {
	m.mu.Lock()
	st := vp.st
	m.mu.Unlock()
	if st == stPaused {
		m.schedule(&vp.t, func() bool { return m.vp == vp && vp.st == stPaused }, pauseCache, func() {
			m.mu.Lock()
			if m.vp == vp && vp.st == stPaused {
				m.vp = nil
				m.note("virtual", vp.meta.Player.ID, "30 минут на паузе")
			}
			m.mu.Unlock()
		})
		return
	}
	alive := func() bool { return m.vp == vp && vp.st == stPlaying }
	m.schedule(&vp.t, alive, flagDelay, func() {
		m.mu.Lock()
		if !alive() {
			m.mu.Unlock()
			return
		}
		ev := m.vevent(vp, 6, 3, vp.pos(m.Now()))
		m.mu.Unlock()
		m.Emit(ev)
	})
	m.schedule(&vp.t, alive, progressEvery, func() { m.vprogress(vp) })
}

func (m *Machine) vprogress(vp *vplayer) {
	m.mu.Lock()
	if m.vp != vp || vp.st != stPlaying {
		m.mu.Unlock()
		return
	}
	ev := m.vevent(vp, 2, 3, vp.pos(m.Now()))
	m.mu.Unlock()
	m.Emit(ev)
	m.schedule(&vp.t, func() bool { return m.vp == vp && vp.st == stPlaying }, progressEvery, func() { m.vprogress(vp) })
}

// isVirtual — id принадлежит виртуальному плееру.
func (m *Machine) isVirtual(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vp != nil && m.vp.st != stEnded && m.vp.meta.Player.ID == id
}

// virtualCommand отдаёт источнику команду платформы виртуальному плееру. Настоящему mediad она не нужна.
func (m *Machine) virtualCommand(c msg.AudioCommand) bool {
	name := "other"
	switch c.Classify() {
	case msg.CmdPause:
		name = "pause"
	case msg.CmdRelease:
		name = "release"
	case msg.CmdPlay, msg.CmdPrefetch:
		name = "play"
	}
	m.mu.Lock()
	f := m.vcmd
	id := m.vp.meta.Player.ID
	m.mu.Unlock()
	if (name == "pause" || name == "release") && m.carrierArmed() {
		// Платформа запускает audio_play вместо прежнего и перед этим «ставит на паузу» текущий плеер. Источник уже выполнил фразу
		// («дальше», «играй»), и эта пауза остановила бы его снова.
		m.note("virtual-cmd", id, name+" не передана: платформа заменяет запуск")
		m.logf("платформа → виртуальный плеер %s: %s при замене запуска, не передаётся", id, name)
		return true
	}
	m.note("virtual-cmd", id, name)
	m.logf("платформа → виртуальный плеер %s: %s", id, name)
	if f != nil {
		f(name)
	}
	return true
}

// heldBySpotify — сессия платформы сейчас занята под Spotify (плеер Яндекса подменён, а не заиграл настоящий).
func (m *Machine) heldBySpotify() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vp != nil && m.vp.adopted && m.vp.st != stEnded
}

// takePrefetched забывает заранее открытые треки очереди Яндекса (m.mu занят) и возвращает их id: закрыть их надо уже без блокировки. Сессию платформы
// заняла музыка, которую ведёт не автомат, и до этих треков очередь не дойдёт, а каждый держит скачивание, процесс-декодер и буфер.
func (m *Machine) takePrefetched() (ids []string) {
	for id, in := range m.players {
		if in.st != stNew {
			continue
		}
		in.t.stop()
		in.st = stEnded
		delete(m.players, id)
		if m.byTrack[in.meta.Player.Stream] == id {
			delete(m.byTrack, in.meta.Player.Stream)
		}
		ids = append(ids, id)
	}
	return ids
}

func (m *Machine) closeAll(ids []string) {
	for _, id := range ids {
		go m.Back.Close(id)
	}
}

// ArmCarrier готовит автомат к тому, что платформа сама запустит музыку Яндекса, хотя Spotify уже выполнил команду («дальше», «назад»,
// «играй» без «Алиса»: платформа ставит audio_play очереди Яндекса). В течение d первый такой запуск (play) не играется: плеер платформы
// занимает Spotify, а предзагрузка (prefetch) молча принимается.
func (m *Machine) ArmCarrier(d time.Duration) {
	m.mu.Lock()
	m.carrierUntil = m.Now().Add(d)
	m.mu.Unlock()
}

func (m *Machine) carrierArmed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.carrierUntil.IsZero() && m.Now().Before(m.carrierUntil)
}

// adoptCommand — команда play или prefetch пришла, пока автомат ждал запуск Яндекса (ArmCarrier).
func (m *Machine) adoptCommand(k msg.CommandKind, c msg.AudioCommand) bool {
	p := c.Player()
	if k == msg.CmdPrefetch {
		m.note("carrier", p.ID, "prefetch принят без звука")
		return true
	}
	m.mu.Lock()
	m.init()
	m.carrierUntil = time.Time{}
	stale := m.takePrefetched()
	meta := MetaFrom(c, false)
	vp := m.vp
	if vp == nil || vp.st == stEnded {
		m.carrier = &meta // Spotify ещё не прислал состояние: плеер достанется ему при первом SetVirtual
		m.mu.Unlock()
		m.closeAll(stale)
		m.note("carrier", p.ID, "плеер платформы ждёт Spotify")
		m.logf("плеер платформы %s занят под Spotify (ждёт состояния)", p.ID)
		return true
	}
	now := m.Now()
	vp.t.stop()
	m.vbind(vp, meta)
	vp.startMs, vp.stopMs = uint64(now.UnixMilli()), 0
	vp.vsid, vp.mark = newVsid(now), randString(8)
	pos := vp.pos(now)
	evs := [][]byte{m.vevent(vp, 1, 1, pos)} // STARTING
	if vp.st == stPlaying {
		evs = append(evs, m.vevent(vp, 1, 3, pos), m.vevent(vp, 2, 3, pos))
	} else {
		vp.stopMs = uint64(now.UnixMilli())
		evs = append(evs, m.vevent(vp, 1, 4, pos))
	}
	m.mu.Unlock()
	m.closeAll(stale)
	m.note("carrier", p.ID, "запуск Яндекса заменён Spotify")
	m.logf("запуск Яндекса %s не играется: плеер платформы теперь Spotify", p.ID)
	for _, e := range evs {
		m.Emit(e)
	}
	m.vtimers(vp)
	return true
}

// TakeOver — Spotify занял место музыки Яндекса. Играющий плеер Яндекса замолкает без события, а платформа дальше считает его текущим,
// только под его id теперь Spotify: так она не держит активной сессию Яндекса (иначе дисплей и облако продолжали бы говорить о ней).
// Если Яндекс не играет, а на паузе у платформы остался его плеер, Spotify занимает и его (иначе дисплей показывает паузу Яндекса).
// Остальные играющие плееры ставятся на паузу как обычно. Возвращает, сколько играющих плееров остановлено.
func (m *Machine) TakeOver() int {
	m.mu.Lock()
	m.init()
	var playing []*inst
	for _, in := range m.players {
		if in.st == stPlaying || in.st == stStarting {
			playing = append(playing, in)
		}
	}
	var cur *inst
	for _, in := range playing {
		if cur == nil || in.startMs > cur.startMs {
			cur = in
		}
	}
	if cur == nil { // ничего не играет, но у платформы может оставаться сессия Яндекса на паузе: Spotify занимает и её
		for _, in := range m.players {
			if in.st == stPaused && (cur == nil || in.stopMs > cur.stopMs) {
				cur = in
			}
		}
		if cur == nil {
			m.mu.Unlock()
			return 0
		}
	}
	id := cur.meta.Player.ID
	cur.t.stop()
	cur.st = stEnded
	delete(m.players, id)
	if m.byTrack[cur.meta.Player.Stream] == id {
		delete(m.byTrack, cur.meta.Player.Stream)
	}
	meta := cur.meta
	var evs [][]byte
	var vp *vplayer
	if m.vp != nil && m.vp.st != stEnded && !m.vp.adopted {
		now := m.Now()
		vp = m.vp
		vp.t.stop()
		m.vbind(vp, meta)
		vp.startMs = cur.startMs
		vp.vsid = cur.vsid
		pos := vp.pos(now)
		if vp.st == stPlaying { // плеер платформы уже играл: продолжаем без STARTING
			evs = append(evs, m.vevent(vp, 1, 3, pos), m.vevent(vp, 2, 3, pos))
		} else {
			vp.stopMs = uint64(now.UnixMilli())
			evs = append(evs, m.vevent(vp, 1, 4, pos))
		}
	} else if m.vp == nil || m.vp.st == stEnded {
		m.carrier = &meta
	}
	m.note("takeover", id, "плеер Яндекса отдан Spotify")
	stale := m.takePrefetched()
	m.mu.Unlock()
	go m.Back.Close(id)
	m.closeAll(stale)
	m.logf("плеер Яндекса %s отдан Spotify", id)
	for _, e := range evs {
		m.Emit(e)
	}
	if vp != nil {
		m.vtimers(vp)
	}
	var others []string
	m.mu.Lock()
	for oid, in := range m.players {
		if in.st == stPlaying || in.st == stStarting {
			others = append(others, oid)
		}
	}
	m.mu.Unlock()
	for _, oid := range others {
		m.Pause(oid)
	}
	return len(playing)
}

// dropAdopted забывает занятый под Spotify плеер платформы: платформа запустила настоящую музыку, прежняя сессия больше не её.
func (m *Machine) dropAdopted() {
	m.mu.Lock()
	if vp := m.vp; vp != nil && vp.adopted {
		vp.t.stop()
		m.vp = nil
		m.note("virtual", vp.meta.Player.ID, "платформа запустила другую музыку")
	}
	m.carrier = nil
	m.mu.Unlock()
}

// retitle подставляет в дескриптор трека (поле 9: 1 — название, 2 — исполнитель) название и исполнителя источника.
func retitle(desc []byte, title, artist string) []byte {
	var out []byte
	done := false
	r := pb.NewReader(desc)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		if f.Num == 9 && f.Wire == pb.WireBytes && !done {
			f.B = retitleMeta(f.B, title, artist)
			done = true
		}
		out = pb.AppendField(out, f)
	}
	if !done {
		out = pb.AppendBytes(out, 9, retitleMeta(nil, title, artist))
	}
	return out
}

func retitleMeta(md []byte, title, artist string) []byte {
	var out []byte
	var has1, has2 bool
	r := pb.NewReader(md)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		switch {
		case f.Num == 1 && f.Wire == pb.WireBytes && !has1:
			f.B, has1 = []byte(title), true
		case f.Num == 2 && f.Wire == pb.WireBytes && !has2:
			f.B, has2 = []byte(artist), true
		}
		out = pb.AppendField(out, f)
	}
	if !has1 {
		out = pb.AppendString(out, 1, title)
	}
	if !has2 {
		out = pb.AppendString(out, 2, artist)
	}
	return out
}

// handoffVirtual останавливает виртуальный плеер при остановке шима: платформа не должна считать, что Spotify играет. Вызывать под m.mu.
func (m *Machine) handoffVirtual() []byte {
	vp := m.vp
	if vp == nil || vp.st != stPlaying {
		return nil
	}
	return m.vpause(vp, m.Now())
}
