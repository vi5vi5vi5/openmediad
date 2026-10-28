package mediad

// Плеер музыки (вид 1, audio): prefetch, play, pause, release, crossfade, перемотка. Команды разбирает этот файл,
// события по таймерам и ответы бэкенда — audio_events.go.

import (
	"fmt"
	"time"

	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/pb"
)

// Тайминги штатного mediad, снятые с записей (msess1): через сколько после команды приходят события.
const (
	startingDelay = 14 * time.Millisecond  // play → STARTING
	warmPlaying   = 7 * time.Millisecond   // STARTING → PLAYING, если плеер уже готов (prefetch / кэш)
	pausedDelay   = 24 * time.Millisecond  // pause → PAUSED
	flagDelay     = 290 * time.Millisecond // PLAYING → первый flag
	progressEvery = 1060 * time.Millisecond

	// pauseCache — сколько плеер на паузе остаётся готовым к возобновлению. У mediad PCM закрывается примерно через
	// 30 минут простоя; больше одного плеера на паузе он не держит.
	pauseCache = 30 * time.Minute

	// openTimeout — сколько ждём, пока запущенный плеер заиграет (поток открыт, звук пошёл), прежде чем вернуть play настоящему mediad.
	openTimeout = 15 * time.Second
)

// DuckGain — громкость музыки по умолчанию, пока у диалога аудиофокус (команда focus 39:1); меняется настройкой
// focus.duck_gain (Machine.Duck). Значение — приближение: в
// записях шины громкость плеера в событиях не меняется (поле 12 всегда 1.0), mediad приглушает музыку мимо неё.
const DuckGain = 0.25

// Backend — то, чем машина пользуется для настоящего звука. Вызовы быстрые, блокировок не берут.
// Результаты асинхронные и возвращаются методами Machine: Opened, Failed, Finished.
type Backend interface {
	Open(id string, t Track, offsetMs uint64) // начать загрузку (prefetch или play)
	Adopt(from, to string, offsetMs uint64)   // отдать открытый источник новому плееру (возобновление с паузы)
	Start(id string)                          // начать/продолжить вывод
	Pause(id string)                          // остановить вывод, источник и позицию держать
	Close(id string)                          // освободить всё
	Seek(id string, posMs uint64)             // перейти к позиции в открытом плеере
	Duck(gain float64)                        // громкость музыки
	Pos(id string) uint64                     // текущая позиция, мс
	Net(id string) (got, total uint64)        // скачано байт / длина потока (для diag)
}

// Track — описание трека для бэкенда.
type Track struct {
	ID      string // идентификатор трека (1.1)
	URL     string // основной URL (1.2)
	Alts    []string
	Key     []byte // AES-128 ключ потока (1.39, hex в команде)
	Bitrate uint64 // 1.45, кбит/с
	Title   string
	// GainDB — поправка громкости трека к целевой −14 LUFS (поле 21: пик и громкость трека). Формула — по смыслу
	// полей (в логах прошивки нет): громкие треки приглушаются, усиление не выше запаса до пика.
	GainDB float64
}

// NormTarget — целевая громкость нормализации, LUFS (поле 21.3 в событиях mediad).
const NormTarget = -14.0

// normGain считает поправку громкости по полю 21 дескриптора: f1 — истинный пик, дБ; f2 — громкость трека, LUFS.
func normGain(desc []byte) float64 {
	n, ok := pb.Bytes(desc, 21)
	if !ok {
		return 0
	}
	a, ok1 := pb.Find(n, 1)
	b, ok2 := pb.Find(n, 2)
	if !ok1 || !ok2 {
		return 0
	}
	peak, loud := float64frombits(a.U), float64frombits(b.U)
	g := NormTarget - loud
	if peak+g > 0 {
		g = -peak
	}
	if g > 12 {
		g = 12
	}
	return g
}

type state uint8

const (
	stNew      state = iota // prefetch получен, play ещё нет
	stStarting              // play получен
	stPlaying
	stPausing // команда pause принята, событие PAUSED ещё не отправлено
	stPaused
	stEnded
)

type inst struct {
	meta     Meta
	raw      []byte // последняя команда (prefetch/play) целиком: если бэкенд не справился, её получает mediad
	ctx      any    // откуда пришла команда (для Forward)
	st       state
	started  bool // событие STARTING уже ушло
	opened   bool
	failed   bool
	durMs    uint64
	startMs  uint64 // поле 5 события
	stopMs   uint64 // поле 7
	seenProg bool
	vsid     string
	mark     string
	over     []pb.Field
	offset   uint64
	t        timers // таймеры плеера: прогресс, flag, отложенные события
}

func (m *Machine) isForeign(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.foreign[id]
}

func (m *Machine) owns(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.players[id]
	return ok
}

// TrackOf разбирает дескриптор команды в Track для бэкенда.
func TrackOf(desc []byte) Track {
	var t Track
	t.ID, _ = pb.Str(desc, 1)
	t.URL, _ = pb.Str(desc, 2)
	t.Bitrate, _ = pb.Uint(desc, 45)
	t.GainDB = normGain(desc)
	if k, ok := pb.Str(desc, 39); ok {
		t.Key = unhex(k)
	}
	if md, ok := pb.Bytes(desc, 9); ok {
		t.Title, _ = pb.Str(md, 1)
	}
	r := pb.NewReader(desc)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		if f.Num == 42 {
			t.Alts = append(t.Alts, string(f.B))
		}
	}
	return t
}

// Prefetch — команда prefetch: открыть трек заранее. События mediad при этом не шлёт.
func (m *Machine) Prefetch(ctx any, c msg.AudioCommand) {
	c = append(msg.AudioCommand(nil), c...) // буфер кадра принадлежит шиму и будет переиспользован
	m.mu.Lock()
	m.init()
	meta := MetaFrom(c, true)
	id := meta.Player.ID
	if old := m.players[id]; old != nil {
		// Платформа присылает prefetch того же плеера второй раз незадолго до конца текущего трека (замечено на записи длинного
		// прогона). Это не новый плеер: поток уже открыт, и забывать об этом нельзя, иначе play будет ждать открытия вечно.
		old.raw, old.ctx = append([]byte(nil), c...), ctx
		old.meta.Desc = meta.Desc
		m.note("prefetch", id, "повторная команда, состояние плеера сохранено")
		m.mu.Unlock()
		return
	}
	in := &inst{meta: meta, raw: append([]byte(nil), c...), ctx: ctx, st: stNew, vsid: newVsid(m.Now()), mark: randString(6)}
	m.players[id] = in
	off, _ := pb.Uint(meta.Desc, 3)
	m.note("prefetch", id, TrackOf(meta.Desc).Title)
	m.mu.Unlock()
	m.Back.Open(id, TrackOf(meta.Desc), off)
}

// Play — команда play. Возвращает false, если запуск отдан настоящему mediad (открыть поток не удалось).
func (m *Machine) Play(ctx any, c msg.AudioCommand) bool {
	c = append(msg.AudioCommand(nil), c...) // буфер кадра принадлежит шиму и будет переиспользован
	m.mu.Lock()
	m.init()
	id := c.Player().ID
	in := m.players[id]
	if in != nil && in.failed {
		delete(m.players, id)
		m.foreign[id] = true
		m.mu.Unlock()
		return false
	}
	var adopt string
	if in == nil {
		track := c.Player().Stream
		var cached *inst
		if pid, ok := m.byTrack[track]; ok {
			cached = m.players[pid]
			adopt = pid
		}
		in = &inst{meta: MetaFrom(c, cached != nil), vsid: newVsid(m.Now()), mark: randString(6)}
		m.players[id] = in
		if cached != nil {
			// mediad возобновляет готовый плеер: в событиях остаётся дескриптор прежнего открытия (URL, зеркала, vsid).
			in.over = descURLFields(cached.meta.Desc, cached.over)
			in.vsid, in.mark = cached.vsid, cached.mark
			in.opened, in.durMs = cached.opened, cached.durMs
			cached.st = stEnded
			cached.t.stop()
			delete(m.players, adopt)
			delete(m.byTrack, track)
		}
	} else {
		in.meta = MetaFrom(c, true) // дескриптор play точнее, чем у prefetch
	}
	in.raw, in.ctx = append([]byte(nil), c...), ctx
	in.offset, _ = pb.Uint(in.meta.Desc, 3)
	// Остальные плееры уступают: идущий или останавливаемый получает ENDED (после PAUSED, если трек тот же).
	var out [][]byte
	for oid, o := range m.players {
		if oid == id || o.st == stEnded || o.st == stNew || o.st == stPaused {
			continue
		}
		out = append(out, m.retire(o, o.meta.Player.Stream == in.meta.Player.Stream)...)
		delete(m.players, oid)
	}
	in.st = stStarting
	in.startMs = uint64(m.Now().UnixMilli()) + uint64(startingDelay/time.Millisecond)
	evict := m.evictPaused(in.meta.Player.Stream, id) // пользователь ушёл на другой трек: кэш паузы больше не нужен
	switch {
	case adopt != "":
		m.note("play-resume", id, fmt.Sprintf("с %d мс, источник плеера %s", in.offset, adopt[:min(8, len(adopt))]))
	case in.opened:
		m.note("play-warm", id, TrackOf(in.meta.Desc).Title)
	default:
		m.note("play-cold", id, TrackOf(in.meta.Desc).Title)
	}
	m.mu.Unlock()
	for _, eid := range evict {
		m.Back.Close(eid)
	}

	switch {
	case adopt != "":
		m.Back.Adopt(adopt, id, in.offset)
	case in.meta.Reused && in.opened:
		m.Back.Seek(id, in.offset)
	case !in.opened && !m.hasSource(id):
		m.Back.Open(id, TrackOf(in.meta.Desc), in.offset)
	}
	m.Back.Duck(m.gain())
	for _, e := range out {
		m.Emit(e)
	}
	m.schedule(&in.t, nil, startingDelay, func() { m.starting(id) })
	m.schedule(&in.t, nil, openTimeout, func() { m.openTimeoutFired(id) })
	return true
}

// openTimeoutFired возвращает команду настоящему mediad, если плеер за openTimeout так и не заиграл: тишина хуже отката.
func (m *Machine) openTimeoutFired(id string) {
	m.mu.Lock()
	in := m.players[id]
	stuck := in != nil && in.st == stStarting
	m.mu.Unlock()
	if stuck {
		m.Failed(id, fmt.Errorf("плеер не заиграл за %s", openTimeout))
	}
}

func (m *Machine) hasSource(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.players[id]
	return in != nil && (in.opened || in.t.active() || in.st == stNew)
}

func (m *Machine) gain() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ducked {
		return m.duckGain()
	}
	return 1
}

// retire завершает плеер, вытесненный новым play. Вызывать под m.mu; возвращает события к отправке.
func (m *Machine) retire(o *inst, sameTrack bool) (out [][]byte) {
	id := o.meta.Player.ID
	o.t.stop()
	pos := m.Back.Pos(id)
	now := uint64(m.Now().UnixMilli())
	m.note("retire", id, fmt.Sprintf("вытеснен новым play на %d мс (тот же трек: %v)", pos, sameTrack))
	if o.st == stPausing && sameTrack {
		o.stopMs = now
		out = append(out, m.event(o, 1, 4, pos))
	}
	if o.stopMs == 0 {
		o.stopMs = now
	}
	out = append(out, m.event(o, 1, 5, pos))
	o.st = stEnded
	go m.Back.Close(id)
	return out
}

// Pause — команда pause: поток замирает, через pausedDelay уходит PAUSED.
func (m *Machine) Pause(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPlaying && in.st != stStarting {
		m.mu.Unlock()
		return
	}
	in.t.stop()
	in.st = stPausing
	m.note("pause", id, "")
	m.mu.Unlock()
	m.Back.Pause(id)
	m.schedule(&in.t, nil, pausedDelay, func() { m.paused(id) })
}

// Release — команда release: отмена prefetch либо освобождение плеера.
func (m *Machine) Release(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil {
		m.mu.Unlock()
		return
	}
	in.t.stop()
	if m.byTrack[in.meta.Player.Stream] == id {
		delete(m.byTrack, in.meta.Player.Stream)
	}
	delete(m.players, id)
	m.note("release", id, "")
	m.mu.Unlock()
	m.Back.Close(id)
}

// Crossfade — команда crossfade: с этого момента mediad показывает в дескрипторе поля 35 и 46.
func (m *Machine) Crossfade(c msg.AudioCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.players[c.Player().ID]
	cf, ok := pb.Bytes(c, 50)
	if in == nil || !ok {
		return
	}
	a, _ := pb.Uint(cf, 1)
	z, _ := pb.Uint(cf, 3)
	f35 := pb.AppendUint(nil, 1, 1)
	f35 = pb.AppendBytes(f35, 2, pb.AppendUint(pb.AppendUint(nil, 3, a), 4, z))
	in.over = setOver(in.over, pb.Field{Num: 35, Wire: pb.WireBytes, B: f35}, pb.Field{Num: 46, Wire: pb.WireVarint, U: a})
}

// Seek — относительная перемотка (AUDIO_REWIND): позиция сдвигается сразу и сообщается дважды (как у mediad),
// дальше platformd пришлёт pause и play со смещением.
func (m *Machine) Seek(id string, deltaMs int64) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPlaying {
		m.mu.Unlock()
		return
	}
	pos := int64(m.Back.Pos(id)) + deltaMs
	if pos < 0 {
		pos = 0
	}
	in.seenProg = false
	ev := m.event(in, 2, 3, uint64(pos))
	m.note("seek", id, fmt.Sprintf("%+d мс → %d мс", deltaMs, pos))
	m.mu.Unlock()
	m.Back.Seek(id, uint64(pos))
	m.Emit(ev)
	m.Emit(ev)
}

// evictPaused снимает с учёта плееры на паузе, кроме id except и плееров трека keepTrack; возвращает их id, которые
// надо закрыть в бэкенде (вне блокировки). Вызывать под m.mu.
func (m *Machine) evictPaused(keepTrack, except string) (ids []string) {
	for pid, o := range m.players {
		if pid == except || o.st != stPaused || keepTrack != "" && o.meta.Player.Stream == keepTrack {
			continue
		}
		o.t.stop()
		if m.byTrack[o.meta.Player.Stream] == pid {
			delete(m.byTrack, o.meta.Player.Stream)
		}
		delete(m.players, pid)
		m.note("evict", pid, "кэш паузы заменён другим плеером")
		ids = append(ids, pid)
	}
	return ids
}
