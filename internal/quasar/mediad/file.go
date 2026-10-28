package mediad

import (
	"fmt"
	"strings"
	"time"

	"openmediad/internal/quasar/msg"
)

// Плеер файлов (вид 4) играет локальные файлы из /system/vendor/quasar/sounds/. Поле 1.50 стартовой команды решает, как кончается
// звук (docs/BUS-PROTOCOL.md, §2.6):
//   - звонок таймера и будильника (50:0, timer.wav — по расширению «wav», по содержимому MP3) звучит по кругу, пока платформа
//     не пришлёт release. Команды: старт (`1:{4:4 19:0 25:путь 50:0}`, плеер `{1:4, 2:uuid, 4:1}`) и release. События по записям
//     (9 звонков, 908 событий): STARTING через ≈27 мс, затем progress в состоянии STARTING, PLAYING и первый progress (длительность
//     файла становится известна), diag с битрейтом, flag, progress примерно раз в 1,05 с;
//   - одноразовый системный звук (50:1: звук после «Алиса», отмена, щелчок громкости, сигналы Bluetooth) звучит один раз, и конец
//     сообщает сам плеер: STARTING, PLAYING, progress (позиция 0), diag, flag, ENDED по концу звука; release платформа присылает
//     только после ENDED (по записям 28.10.2026: 16 звуков).
//
// Во всех событиях после STARTING mediad повторяет один и тот же uuid (поле 3.22); у STARTING он свой. Спектра у плеера файлов нет.
const (
	fileStartingDelay = 25 * time.Millisecond
	fileFlagDelay     = 240 * time.Millisecond // звонок: PLAYING → flag
	fileOnceFlagDelay = 210 * time.Millisecond // одноразовый звук: PLAYING → flag (медиана по записям; короткий звук кончается раньше)
	fileProgressEvery = 1050 * time.Millisecond
	fileOpenTimeout   = 3 * time.Second
	fileForgetAfter   = 30 * time.Second // одноразовый звук после ENDED ждёт release платформы (она шлёт его через 3–10 мс)
)

// FileBackend — звук плеера файлов. Результаты возвращаются методами Machine: FilePlaying, FileEnded, FileFailed.
type FileBackend interface {
	Start(id, path string, loop bool) // раскодировать файл и играть: по кругу (loop) или один раз
	Stop(id string)                   // оборвать сразу
	Pos(id string) uint64             // позиция внутри текущего прохода файла, мс
	Elapsed(id string) uint64         // сколько всего звучит, мс
}

// DefaultFileOK — какие файлы шим берёт на себя: только системные звуки колонки. Всё остальное идёт настоящему mediad.
func DefaultFileOK(path string) bool {
	const dir = "/system/vendor/quasar/sounds/"
	return strings.HasPrefix(path, dir) && !strings.Contains(path, "..") && !strings.ContainsAny(path, " \t\n\"'`$;|&<>\\")
}

type finst struct {
	meta     PlainMeta
	path     string
	once     bool   // одноразовый звук (50:1): ENDED по концу звука, а не по release
	raw      []byte // стартовая команда: если бэкенд не справился (или шим останавливается), её получает mediad
	ctx      any
	st       vstate
	startMs  uint64
	stopMs   uint64
	uuid     string // поле 3.22 всех событий после STARTING
	vsid     string
	mark     string
	lastProg uint64
	seenProg bool   // первый progress в состоянии PLAYING уже ушёл: у него поле 9 = 0, у следующих 1
	wrapped  bool   // файл начался заново после прошлого progress: у этого progress поле 9 снова 0
	starting bool   // событие STARTING уже ушло
	durMs    uint64 // длительность файла, мс (0 — ещё неизвестна)
	durKnown bool   // поле 3.6 уже несёт длительность (до первого progress в PLAYING там 0)
	bitrate  uint64
	bitKnown bool // diag bitrate появляется после первого progress в PLAYING
	t        timers
}

// fafter запускает fn через d как таймер звонка. Если звонок уже снят (release пришёл между вызовами), таймер отменяется сразу.
func (m *Machine) fafter(id string, f *finst, d time.Duration, fn func()) {
	m.schedule(&f.t, func() bool { return m.files[id] == f }, d, fn)
}

// fileStart обрабатывает стартовую команду плеера файлов. false — файл не из разрешённых или плеер отдан mediad.
func (m *Machine) fileStart(ctx any, c msg.AudioCommand) bool {
	path, _ := c.FilePath()
	ok := m.FileOK
	if ok == nil {
		ok = DefaultFileOK
	}
	if !ok(path) {
		return false
	}
	c = append(msg.AudioCommand(nil), c...) // буфер кадра принадлежит шиму
	id := c.Player().ID
	once := !c.FileLoops()
	m.mu.Lock()
	m.init()
	if m.fforeign[id] {
		m.mu.Unlock()
		return false
	}
	if m.files[id] != nil { // платформа повторила стартовую команду: звук уже идёт, второй не нужен
		m.note("file-start", id, "повторная команда, звук уже идёт")
		m.mu.Unlock()
		return true
	}
	now := m.Now()
	f := &finst{meta: PlainMetaFrom(c), path: path, once: once, raw: c, ctx: ctx, startMs: uint64(now.UnixMilli()),
		uuid: newUUID(), vsid: newVsid(now), mark: randString(6)}
	m.files[id] = f
	if once {
		m.note("file-once", id, path)
	} else {
		m.note("file-start", id, path)
	}
	m.mu.Unlock()
	m.File.Start(id, path, !once)
	m.fafter(id, f, fileStartingDelay, func() { m.fileStarting(id) })
	m.fafter(id, f, fileOpenTimeout, func() { m.fileTimeout(id) })
	return true
}

// fileRelease — команда release плеера файлов: останавливает звонок и сообщает платформе ENDED. У одноразового звука release
// приходит после ENDED: тогда событий больше нет, плеер просто забывается.
func (m *Machine) fileRelease(id string) bool {
	m.mu.Lock()
	m.init()
	if m.fforeign[id] {
		delete(m.fforeign, id)
		m.mu.Unlock()
		return false
	}
	f := m.files[id]
	if f == nil {
		m.mu.Unlock()
		return false
	}
	f.t.stop()
	delete(m.files, id)
	var ev []byte
	if f.st != vEnded {
		f.stopMs = uint64(m.Now().UnixMilli())
		f.st = vEnded
		ev = m.fileEvent(id, f, 1, 5, f.lastProg)
	}
	m.note("file-release", id, "")
	m.mu.Unlock()
	m.File.Stop(id)
	if ev != nil {
		m.Emit(ev)
	}
	return true
}

func (m *Machine) fileStarting(id string) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st != vStarting || f.starting {
		m.mu.Unlock()
		return
	}
	f.starting = true
	ev := m.fileEvent(id, f, 1, 1, 0)
	m.mu.Unlock()
	m.Emit(ev)
}

// FilePlaying вызывает бэкенд, когда файл раскодирован и первый звук ушёл на динамик.
func (m *Machine) FilePlaying(id string, durMs, bitrate uint64) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st != vStarting {
		m.mu.Unlock()
		return
	}
	f.durMs, f.bitrate = durMs, bitrate
	var evs [][]byte
	if !f.starting { // звук пошёл раньше таймера STARTING: порядок событий штатный в любом случае
		f.starting = true
		evs = append(evs, m.fileEvent(id, f, 1, 1, 0))
	}
	if !f.once {
		evs = append(evs, m.fileEvent(id, f, 2, 1, 0)) // progress в состоянии STARTING: длительность пока 0
	}
	f.st = vPlaying
	evs = append(evs, m.fileEvent(id, f, 1, 3, 0)) // PLAYING: у звонка длительность 0, у одноразового звука поля 6 ещё нет
	f.durKnown = true
	evs = append(evs, m.fileEvent(id, f, 2, 3, 0)) // первый progress: длительность известна, поле 9 = 0
	f.seenProg, f.bitKnown = true, true
	if f.bitrate > 0 { // mediad сообщает битрейт отдельным событием diag, как только его узнал
		evs = append(evs, f.meta.Diag(PlainSnap{State: 3, Vsid: f.vsid, Mark: f.mark, RSS: syntheticRSS, Bitrate: f.bitrate}))
	}
	m.note("file-playing", id, fmt.Sprintf("длительность %d мс", durMs))
	flag := fileFlagDelay
	if f.once {
		flag = fileOnceFlagDelay
	}
	m.mu.Unlock()
	for _, e := range evs {
		m.Emit(e)
	}
	m.fafter(id, f, flag, func() { m.fileFlag(id) })
	m.fafter(id, f, fileProgressEvery, func() { m.fileProgress(id) })
}

// FileEnded вызывает бэкенд, когда одноразовый звук доиграл до конца: ENDED с позицией последнего progress. Плеер остаётся до
// release платформы (она присылает его следом), чтобы release не ушёл mediad.
func (m *Machine) FileEnded(id string) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st != vPlaying {
		m.mu.Unlock()
		return
	}
	f.t.stop()
	f.st = vEnded
	f.stopMs = uint64(m.Now().UnixMilli())
	ev := m.fileEvent(id, f, 1, 5, f.lastProg)
	m.note("file-ended", id, "звук доиграл")
	m.mu.Unlock()
	m.Emit(ev)
	m.fafter(id, f, fileForgetAfter, func() { // release не пришёл: не держать плеер вечно
		m.mu.Lock()
		if m.files[id] == f {
			delete(m.files, id)
		}
		m.mu.Unlock()
	})
}

func (m *Machine) fileFlag(id string) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st != vPlaying {
		m.mu.Unlock()
		return
	}
	ev := m.fileEvent(id, f, 6, 3, f.lastProg)
	m.mu.Unlock()
	m.Emit(ev)
}

func (m *Machine) fileProgress(id string) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st != vPlaying {
		m.mu.Unlock()
		return
	}
	pos := m.File.Pos(id)
	if pos < f.lastProg { // файл начался заново: у первого progress нового прохода поле 9 = 0
		f.wrapped = true
	}
	f.lastProg = pos
	ev := m.fileEvent(id, f, 2, 3, pos)
	f.wrapped = false
	m.mu.Unlock()
	m.Emit(ev)
	m.fafter(id, f, fileProgressEvery, func() { m.fileProgress(id) })
}

// FileFailed вызывает бэкенд (или таймер), когда звонок не удалось начать: стартовая команда уходит настоящему mediad,
// и release этого плеера тоже. Если звук уже шёл, звонок считается законченным.
func (m *Machine) FileFailed(id string, err error) {
	m.mu.Lock()
	f := m.files[id]
	if f == nil || f.st == vEnded {
		m.mu.Unlock()
		return
	}
	m.logf("плеер файлов %s: %v", id, err)
	m.note("file-failed", id, err.Error())
	f.t.stop()
	if f.st == vPlaying {
		f.st = vEnded
		f.stopMs = uint64(m.Now().UnixMilli())
		ev := m.fileEvent(id, f, 1, 5, f.lastProg)
		m.mu.Unlock()
		m.Emit(ev)
		return
	}
	raw, ctx := f.raw, f.ctx
	delete(m.files, id)
	m.fforeign[id] = true
	m.mu.Unlock()
	m.File.Stop(id)
	m.forward(ctx, raw)
}

func (m *Machine) fileTimeout(id string) {
	m.mu.Lock()
	f := m.files[id]
	stuck := f != nil && f.st == vStarting
	m.mu.Unlock()
	if stuck {
		m.FileFailed(id, fmt.Errorf("звонок не заиграл за %s", fileOpenTimeout))
	}
}

// fileEvent собирает событие плеера файлов. Вызывать под m.mu.
func (m *Machine) fileEvent(id string, f *finst, kind, st, posMs uint64) []byte {
	s := PlainSnap{
		Kind: msg.EventKind(kind), State: msg.PlayState(st),
		PosMs: posMs, Idx: posMs / 1000, UUID: f.uuid,
		Start: f.startMs, Stop: f.stopMs, Vsid: f.vsid, Mark: f.mark, RSS: syntheticRSS,
	}
	starting := kind == 1 && st == 1
	if starting {
		s.UUID = newUUID() // у STARTING свой uuid, у всех следующих событий — общий
	}
	// у STARTING-как-смены-состояния нет ни длительности, ни монотонного времени; у одноразового звука их нет и у PLAYING
	// (у звонка до PLAYING успевает уйти progress, у одноразового звука — нет)
	if !starting && !(f.once && kind == 1 && st == 3) {
		s.Has6, s.Mono = true, m.Mono()
		if f.durKnown {
			s.Dur6 = f.durMs / 1000 // целые секунды без округления: у spotter_start.mp3 (0,58 с) mediad показывает 0
		}
	}
	if f.bitKnown && f.bitrate > 0 && (kind == 6 || kind == 2 || st == 5) {
		s.Bitrate = f.bitrate
	}
	switch kind {
	case 2:
		s.HasT9, s.T9 = true, f.seenProg && !f.wrapped
	case 6:
		s.HasT17, s.T17 = true, true
	}
	if el := m.File.Elapsed(id); el >= 1000 { // played_sec — полных секунд звучания
		s.HasPlayed, s.PlayedSec = true, el/1000
	}
	return f.meta.Event(s)
}

// fileHand — звонок, который надо передать настоящему mediad при остановке шима.
type fileHand struct {
	id  string
	raw []byte
	ctx any
}

// handoffFiles снимает идущие звуки (под m.mu). Платформа считает звонок идущим и событий конца не ждёт: ENDED не шлём,
// стартовую команду после остановки звука получает настоящий mediad, и звонок продолжается у него (handoffFilesDone).
// Одноразовый звук, который уже звучал, второй раз не играется: он обрывается и получает ENDED (его возвращают в evs);
// не успевший зазвучать отдаётся mediad, как звонок. Закончившийся звук (ENDED уже ушёл) не передаётся никому: иначе mediad
// зазвонил бы заново звонком, который платформа считает законченным.
func (m *Machine) handoffFiles() (out []fileHand, stop []string, evs [][]byte) {
	for id, f := range m.files {
		f.t.stop()
		delete(m.files, id)
		switch {
		case f.st == vEnded:
		case f.once && f.st == vPlaying:
			f.st, f.stopMs = vEnded, uint64(m.Now().UnixMilli())
			evs = append(evs, m.fileEvent(id, f, 1, 5, f.lastProg))
			stop = append(stop, id)
			m.note("handoff", id, "шим останавливается: системный звук оборван")
		default:
			m.note("handoff", id, "шим останавливается: звук передан mediad")
			out = append(out, fileHand{id, f.raw, f.ctx})
		}
	}
	return out, stop, evs
}

func (m *Machine) handoffFilesDone(hs []fileHand, stop []string) {
	for _, id := range stop {
		m.File.Stop(id)
	}
	for _, h := range hs {
		m.File.Stop(h.id)
		m.forward(h.ctx, h.raw)
	}
}

// fileInfos — звонки и системные звуки для отладочной страницы.
func (m *Machine) fileInfos() (out []PlayerInfo) {
	for id, f := range m.files {
		what := "звонок"
		if f.once {
			what = "системный звук"
		}
		name := what + ": запускается"
		switch f.st {
		case vPlaying:
			name = what + ": звучит"
		case vEnded:
			name = what + ": доиграл"
		}
		out = append(out, PlayerInfo{ID: id[:min(8, len(id))], State: name, Title: f.path, PosMs: m.File.Pos(id), DurMs: f.durMs})
	}
	return out
}
