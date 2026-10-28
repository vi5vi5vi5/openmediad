// Package mediad — эмуляция штатного проигрывателя mediad: принимает команды platformd (тип 8) и выпускает события плеера
// (тип 127) тем же языком, байт в байт. Это ядро совместимости: правила выведены из записей живого трафика и сверены с
// ними побайтно (золотые тесты *_test.go); схем в прошивке нет, поэтому правила — наблюдение, а не спецификация.
// Справочник по сообщениям — docs/BUS-PROTOCOL.md.
//
// Три вида плееров — по файлу на каждый: audio.go (музыка, вид 1), voice.go (голос Алисы, вид 2), file.go (звонки, вид 4).
// Звук автомат не играет: он отдаёт его бэкендам (Backend, VoiceBackend, FileBackend), а они сообщают о результатах
// методами Machine (Opened, VoicePlaying, FilePlaying…).
package mediad

import (
	"fmt"
	"sync"
	"time"

	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/pb"
)

// Machine — модель набора плееров mediad: принимает команды platformd и выпускает события тем же языком.
type Machine struct {
	// Emit отправляет тело AudioClientEvent всем клиентам. Вызывается без удерживаемых блокировок машины,
	// возможно из разных горутин: реализация должна быть потокобезопасной.
	Emit func(body []byte)
	// Forward отдаёт команду настоящему mediad (откат: бэкенд не смог открыть поток). ctx — то, что передали в Handle.
	Forward func(ctx any, body []byte)
	// Now — текущее время; Mono — CLOCK_MONOTONIC в мс; After — запуск f через d (возвращает отмену).
	Now   func() time.Time
	Mono  func() uint64
	After func(d time.Duration, f func()) (cancel func())
	Back  Backend
	Logf  func(string, ...any)
	// Note (необязательный) получает короткие сведения о решениях автомата для отладочного журнала. Вызывается под
	// блокировкой машины: обработчик не должен обращаться к машине. Виды: prefetch, play-warm, play-cold, play-resume,
	// pause, retire, evict, expire, release, focus, seek, finished, failed.
	Note func(kind, id, detail string)

	// NoMusic отключает перехват музыкального плеера (остаётся только голос и слежение за фокусом).
	NoMusic bool

	// Duck — громкость музыки, пока у диалога аудиофокус; 0 — DuckGain. На ходу меняется только через SetDuck.
	Duck float64

	// Voice (необязательный) включает перехват голоса Алисы: voice-start, voice-chunk, voice-eos и release голосового плеера.
	// nil — голос идёт настоящему mediad.
	Voice VoiceBackend

	// File (необязательный) включает перехват плеера файлов (вид 4): звонок таймера и будильника, системные звуки.
	// nil — они идут настоящему mediad. FileOK решает, какие файлы брать себе (nil — DefaultFileOK).
	File   FileBackend
	FileOK func(path string) bool

	mu       sync.Mutex
	voices   map[string]*vinst
	vforeign map[string]bool // ответы, отданные настоящему mediad (откат): остальные команды ответа тоже идут ему
	files    map[string]*finst
	fforeign map[string]bool // звонки, отданные настоящему mediad: release тоже идёт ему
	players  map[string]*inst
	byTrack  map[string]string // трек → id его плеера на паузе (кэш mediad)
	foreign  map[string]bool   // плееры, которые ведёт настоящий mediad (откат): их команды не перехватываются
	ducked   bool
}

// SetDuck меняет громкость музыки во время диалога на ходу (настройка focus.duck_gain). Звучащая сейчас приглушённая
// музыка переходит к новой громкости плавно.
func (m *Machine) SetDuck(g float64) {
	m.mu.Lock()
	m.Duck = g
	ducked := m.ducked
	m.mu.Unlock()
	if ducked && m.Back != nil {
		m.Back.Duck(m.gain())
	}
}

// duckGain — громкость музыки во время диалога. Вызывать под m.mu.
func (m *Machine) duckGain() float64 {
	if m.Duck > 0 {
		return m.Duck
	}
	return DuckGain
}

func (m *Machine) note(kind, id, detail string) {
	if m.Note != nil {
		m.Note(kind, id, detail)
	}
}

func (m *Machine) logf(f string, a ...any) {
	if m.Logf != nil {
		m.Logf(f, a...)
	}
}

func (m *Machine) init() {
	if m.players == nil {
		m.players = map[string]*inst{}
		m.byTrack = map[string]string{}
		m.foreign = map[string]bool{}
		m.voices = map[string]*vinst{}
		m.vforeign = map[string]bool{}
		m.files = map[string]*finst{}
		m.fforeign = map[string]bool{}
	}
}

// Handle разбирает команду плееру. Возвращает true, если команда обработана машиной и настоящему mediad
// пересылать её не нужно. Команды чужих плееров, голос и всё незнакомое возвращают false.
func (m *Machine) Handle(ctx any, c msg.AudioCommand) bool {
	p := c.Player()
	switch k := c.Classify(); k {
	case msg.CmdFocus:
		on, _ := pb.Uint(c, 39)
		m.Focus(on == 1)
		return false
	case msg.CmdVoiceStart, msg.CmdVoiceChunk, msg.CmdVoiceEOS:
		if m.Voice == nil || p.Kind != msg.PlayerVoice {
			return false
		}
		return m.voiceCommand(ctx, k, c)
	case msg.CmdFileStart:
		if m.File == nil {
			return false
		}
		return m.fileStart(ctx, c)
	case msg.CmdPlay, msg.CmdPrefetch:
		if m.NoMusic || p.Kind != msg.PlayerAudio || m.isForeign(p.ID) {
			return false
		}
		if k == msg.CmdPrefetch {
			m.Prefetch(ctx, c)
		} else {
			return m.Play(ctx, c)
		}
		return true
	case msg.CmdPause, msg.CmdRelease, msg.CmdCrossfade:
		if k == msg.CmdRelease && p.Kind == msg.PlayerVoice && m.Voice != nil {
			return m.voiceRelease(p.ID)
		}
		if k == msg.CmdRelease && p.Kind == msg.PlayerFile && m.File != nil {
			return m.fileRelease(p.ID)
		}
		if m.NoMusic || p.Kind != msg.PlayerAudio || !m.owns(p.ID) {
			return false
		}
		switch k {
		case msg.CmdPause:
			m.Pause(p.ID)
		case msg.CmdRelease:
			m.Release(p.ID)
		default:
			m.Crossfade(c)
		}
		return true
	case msg.CmdOther:
		if !m.NoMusic && c.Reason() == "AUDIO_REWIND directive" && m.owns(p.ID) {
			if rw, ok := pb.Bytes(c, 13); ok {
				if f, ok := pb.Find(rw, 2); ok && f.Wire == pb.WireFixed64 {
					m.Seek(p.ID, int64(float64frombits(f.U)*1000))
				}
			}
			return true
		}
	}
	return false
}

// Focus — аудиофокус диалога: true приглушает музыку, false возвращает громкость; flag сообщает о смене.
func (m *Machine) Focus(on bool) {
	m.mu.Lock()
	m.init()
	if m.ducked == on {
		m.mu.Unlock()
		return
	}
	m.ducked = on
	m.note("focus", "", fmt.Sprint(on))
	var playing []*inst
	for _, in := range m.players {
		if in.st == stPlaying {
			playing = append(playing, in)
		}
	}
	g := 1.0
	if on {
		g = m.duckGain()
	}
	m.mu.Unlock()
	m.Back.Duck(g)
	for _, in := range playing {
		id := in.meta.Player.ID
		m.schedule(&in.t, nil, 300*time.Millisecond, func() { m.flagEvent(id) })
	}
}

// Handoff — мягкая передача плееров настоящему mediad при остановке шима: каждый играющий плеер получает PAUSED с текущей
// позицией, поэтому платформа считает музыку стоящей на паузе, и «продолжи» запустит трек у настоящего mediad с того же места.
// Без этого платформа продолжила бы считать, что музыка играет, хотя звука уже нет. Вызывать до закрытия соединений шима.
func (m *Machine) Handoff() {
	m.mu.Lock()
	var evs [][]byte
	var ids []string
	for id, in := range m.players {
		if in.st != stPlaying && in.st != stStarting && in.st != stPausing {
			continue
		}
		in.t.stop()
		in.st = stPaused
		in.stopMs = uint64(m.Now().UnixMilli())
		evs = append(evs, m.event(in, 1, 4, m.Back.Pos(id)))
		ids = append(ids, id)
		m.note("handoff", id, "шим останавливается: плеер передан mediad на паузе")
	}
	var vids []string
	for id, v := range m.voices {
		if v.st == vEnded {
			continue
		}
		v.t.stop()
		v.st = vEnded
		v.stopMs = uint64(m.Now().UnixMilli())
		evs = append(evs, m.voiceEvent(v, 1, 5, v.lastProg))
		vids = append(vids, id)
		m.note("handoff", id, "шим останавливается: ответ Алисы прерван")
	}
	fhs, fstop, fevs := m.handoffFiles()
	evs = append(evs, fevs...)
	m.mu.Unlock()
	for _, id := range ids {
		m.Back.Pause(id)
	}
	for _, id := range vids {
		m.Voice.Abort(id)
	}
	m.handoffFilesDone(fhs, fstop)
	for _, e := range evs {
		m.Emit(e)
	}
}

// PlayerInfo — снимок плеера для отладочной страницы.
type PlayerInfo struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Track  string `json:"track,omitempty"`
	Title  string `json:"title,omitempty"`
	PosMs  uint64 `json:"pos_ms"`
	DurMs  uint64 `json:"dur_ms,omitempty"`
	Opened bool   `json:"opened"`
	Ducked bool   `json:"ducked"`
}

var stateNames = [...]string{"открыт заранее (prefetch)", "запускается", "играет", "останавливается", "на паузе", "закончен"}

// Snapshot возвращает плееры, которые сейчас ведёт машина.
func (m *Machine) Snapshot() []PlayerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PlayerInfo, 0, len(m.players))
	for id, in := range m.players {
		t := TrackOf(in.meta.Desc)
		out = append(out, PlayerInfo{ID: id[:min(8, len(id))], State: stateNames[in.st], Track: t.ID, Title: t.Title,
			PosMs: m.Back.Pos(id), DurMs: in.durMs, Opened: in.opened, Ducked: m.ducked})
	}
	for id, v := range m.voices {
		name := "голос: запускается"
		switch v.st {
		case vPlaying:
			name = "голос: говорит"
		case vEnded:
			name = "голос: закончил"
		}
		out = append(out, PlayerInfo{ID: id[:min(8, len(id))], State: name, Title: "ответ Алисы", PosMs: m.Voice.Pos(id)})
	}
	out = append(out, m.fileInfos()...)
	return out
}
