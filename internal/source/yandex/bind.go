package yandex

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/codec/aac"
	"openmediad/internal/engine"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/spectrum"
)

// Привязка к эмуляции mediad: Backend реализует mediad.Backend — плееры музыки (команды prefetch, play, pause, release,
// перемотка) становятся потоками движка класса music. Источник Яндекса существует только как часть протокола Quasar,
// поэтому привязка живёт здесь, рядом с ним (PLAN.md, §5.3).

const (
	// fadeIn — нарастание громкости в начале каждого вывода: без щелчка при старте и после паузы.
	fadeIn = 150 * time.Millisecond
	// posLatency — через сколько после записи в aplay звук слышен: старт PCM и буфер смесителя. По замерам до запуска PCM
	// ядром 14–30 мс; оценка нужна для позиции и возврата звука при паузе.
	posLatency = 40 * time.Millisecond
	// resumeSlack — насколько смещение возобновления может отличаться от места паузы без перемотки (иначе поток
	// открывается заново с нужного места).
	resumeSlack = 1500 * time.Millisecond
)

// MusicEvents — куда Backend сообщает о результатах (это mediad.Machine).
type MusicEvents interface {
	Opened(id string, durMs uint64)
	Failed(id string, err error)
	Finished(id string)
}

// Backend — плееры музыки Яндекса поверх движка.
type Backend struct {
	Engine     *engine.Engine
	Events     MusicEvents
	Client     *http.Client // nil — клиент с сертификатами прошивки (NewClient)
	NewDecoder aac.Factory  // nil — GStreamer колонки
	Logf       func(string, ...any)
	NoFadeIn   bool // для тестов, которые читают отсчёты как метки
	NoRewind   bool // для тестов с мгновенным выводом: возврат звука по часам там неприменим

	mu      sync.Mutex
	players map[string]*player
}

type player struct {
	id     string // текущий id плеера: после Adopt меняется
	st     *engine.Stream
	src    *Source
	gainDB float64
}

var _ mediad.Backend = (*Backend)(nil)

// Warm готовит HTTP-клиент заранее: чтение сертификатов занимает десятки миллисекунд, и первая же команда не должна
// задерживать кадры шины (в экспериментах так задержался кадр на 83 мс).
func (b *Backend) Warm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Client == nil {
		b.Client = NewClient()
	}
}

func (b *Backend) get(id string) *player {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.players[id]
}

// currentID — id плеера на момент события (после Adopt он другой).
func (b *Backend) currentID(p *player) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return p.id
}

// Open начинает загрузку трека (prefetch или play). Повторное открытие уже открытого трека (повторный prefetch) сообщает
// об открытии ещё раз: иначе тот, кто ждёт события, не узнает, что поток давно готов.
func (b *Backend) Open(id string, t mediad.Track, offsetMs uint64) {
	b.Warm()
	b.mu.Lock()
	if old := b.players[id]; old != nil {
		b.mu.Unlock()
		if info, ok := old.st.Ready(); ok {
			go b.Events.Opened(id, uint64(info.Duration/time.Millisecond))
		}
		return
	}
	src := New(Track{ID: t.ID, URL: t.URL, Alts: t.Alts, Key: t.Key, Title: t.Title}, offsetMs,
		Deps{Client: b.Client, NewDecoder: b.NewDecoder, Logf: b.Logf})
	p := &player{id: id, src: src, gainDB: t.GainDB}
	opt := engine.Options{Gain: audio.DB(t.GainDB), Spectrum: spectrum.SourceMusic, RewindOnPause: !b.NoRewind, PosLatency: posLatency}
	if !b.NoFadeIn {
		opt.FadeIn = fadeIn
	}
	p.st = b.Engine.NewStream(id, engine.Music, src, opt, engine.Events{
		Ready:  func(i audio.Info) { b.Events.Opened(b.currentID(p), uint64(i.Duration/time.Millisecond)) },
		Failed: func(err error) { b.Events.Failed(b.currentID(p), err) },
		Ended:  func() { b.Events.Finished(b.currentID(p)) },
	})
	if b.players == nil {
		b.players = map[string]*player{}
	}
	b.players[id] = p
	b.mu.Unlock()
	p.st.Open()
}

// Adopt передаёт открытый источник другому плееру (возобновление с паузы: платформа шлёт play с новым id). Если смещение
// заметно отличается от места паузы, поток перематывается; иначе звук продолжается из буфера без перезапуска.
func (b *Backend) Adopt(from, to string, offsetMs uint64) {
	b.mu.Lock()
	p := b.players[from]
	if p == nil || b.players[to] != nil {
		b.mu.Unlock()
		return
	}
	delete(b.players, from)
	p.id = to
	b.players[to] = p
	b.mu.Unlock()
	want := time.Duration(offsetMs) * time.Millisecond
	if d := want - p.st.Pos(); d > resumeSlack || d < -resumeSlack {
		p.st.Seek(want)
	}
}

// Start начинает или продолжает вывод. Для ещё не открытого трека вывод сам дождётся звука.
func (b *Backend) Start(id string) {
	if p := b.get(id); p != nil {
		p.st.Play()
	}
}

// Pause останавливает вывод; источник, буфер и позиция остаются (как кэш плееров mediad).
func (b *Backend) Pause(id string) {
	if p := b.get(id); p != nil {
		p.st.Pause()
	}
}

// Close освобождает трек целиком: скачивание, декодер, буфер, вывод.
func (b *Backend) Close(id string) {
	b.mu.Lock()
	p := b.players[id]
	delete(b.players, id)
	b.mu.Unlock()
	if p != nil {
		p.st.Close()
	}
}

// Seek переходит к позиции: поток открывается заново с нужного кадра.
func (b *Backend) Seek(id string, posMs uint64) {
	if p := b.get(id); p != nil {
		p.st.Seek(time.Duration(posMs) * time.Millisecond)
	}
}

// Duck задаёт громкость музыки (1 — как есть; меньше — приглушение на время диалога).
func (b *Backend) Duck(g float64) { b.Engine.SetClassGain(engine.Music, g) }

// Pos — позиция воспроизведения, мс.
func (b *Backend) Pos(id string) uint64 {
	if p := b.get(id); p != nil {
		return uint64(p.st.Pos() / time.Millisecond)
	}
	return 0
}

// Net — сколько скачано и длина потока (идёт в диагностику событий).
func (b *Backend) Net(id string) (uint64, uint64) {
	if p := b.get(id); p != nil {
		return p.src.Net()
	}
	return 0, 0
}

// Shutdown закрывает все треки (остановка демона).
func (b *Backend) Shutdown() {
	b.mu.Lock()
	ps := b.players
	b.players = nil
	b.mu.Unlock()
	for _, p := range ps {
		p.st.Close()
	}
}

// TrackInfo — снимок трека для отладочной страницы.
type TrackInfo struct {
	ID      string  `json:"id"`
	Title   string  `json:"title,omitempty"`
	Playing bool    `json:"playing"`
	Opened  bool    `json:"opened"`
	PosMs   uint64  `json:"pos_ms"`
	GotKB   uint64  `json:"downloaded_kb"`
	TotalKB uint64  `json:"total_kb"`
	RingMs  int64   `json:"buffered_ms"`
	NormDB  float64 `json:"norm_db,omitempty"`
}

// Snapshot — открытые треки: сколько скачано, сколько звука в буфере, играет ли.
func (b *Backend) Snapshot() []TrackInfo {
	b.mu.Lock()
	ps := make([]*player, 0, len(b.players))
	for _, p := range b.players {
		ps = append(ps, p)
	}
	b.mu.Unlock()
	out := make([]TrackInfo, 0, len(ps))
	for _, p := range ps {
		_, opened := p.st.Ready()
		got, tot := p.src.Net()
		out = append(out, TrackInfo{ID: short(b.currentID(p)), Title: p.src.t.Title, Playing: p.st.Playing(), Opened: opened,
			PosMs: uint64(p.st.Pos() / time.Millisecond), GotKB: got >> 10, TotalKB: tot >> 10,
			RingMs: int64(p.src.Buffered() / time.Millisecond), NormDB: p.gainDB})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
