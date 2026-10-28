// Package engine — ядро звука: потоки (источник → вывод), классы потоков и их громкость (приглушение музыки на время
// диалога), выбор выхода по классу, позиция по часам, возврат непрозвучавшего звука при паузе, спектр для дисплея.
//
// Движок не знает о шине Quasar: команды платформы переводит в вызовы потоков эмуляция mediad (internal/quasar/mediad,
// через привязки источников). Источники и выходы не знают о движке: они реализуют интерфейсы этого пакета и
// internal/audio структурно.
//
// Жизнь потока: NewStream → Open (источник готовится в фоне: сеть, декодер; потом Events.Ready) → Play (вывод открывается
// сразу, звук пойдёт, как только источник готов) → Pause / Play … → Close. Конец звука — Events.Ended; ошибка до первого звука
// — Events.Failed (повод вернуть команду штатному mediad), после первого звука — тоже Ended (трек считается законченным).
package engine

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/spectrum"
)

// Class — класс потока: определяет громкость (приглушение) и выход по настройке маршрутов.
type Class int

const (
	Music Class = iota // музыка и прочие долгие потоки
	Voice              // голос Алисы
	Alert              // звонки таймеров и будильников, системные сигналы
	numClasses
)

func (c Class) String() string {
	switch c {
	case Music:
		return "music"
	case Voice:
		return "voice"
	case Alert:
		return "alert"
	}
	return "class?"
}

// ParseClass — класс по имени из настроек.
func ParseClass(s string) (Class, bool) {
	for c := Class(0); c < numClasses; c++ {
		if c.String() == s {
			return c, true
		}
	}
	return 0, false
}

// Source — источник звука. Read блокируется, пока данных нет; io.EOF — звук кончился; audio.ErrWake — чтение прервано
// по просьбе движка (Waker).
type Source interface {
	Format() audio.Format
	Open(ctx context.Context) (audio.Info, error)
	Read(p []byte) (int, error)
	Close() error
}

// Необязательные возможности источника.
type (
	// Seeker — перемотка; возвращает позицию, с которой звук действительно пойдёт (начало кадра).
	Seeker interface {
		Seek(pos time.Duration) (time.Duration, error)
	}
	// Rewinder — вернуть n последних отданных байт (звук, который при паузе не дошёл до динамика); возвращает, сколько вернул.
	Rewinder interface{ Rewind(n int) int }
	// Waker — прервать ожидающее чтение (оно вернёт audio.ErrWake), не теряя данных.
	Waker interface{ Wake() }
	// SpectrumTapper — источник отдаёт спектру и то, что в динамик не идёт (голос: отсчёты pre-skip Opus; так считает
	// штатный mediad). Отвод вызывается из Read, в той же горутине, что вывод.
	SpectrumTapper interface{ SetSpectrumTap(func(pcm []byte)) }
)

// Router выбирает выход для класса потока и открывает его под формат потока.
type Router interface {
	Output(c Class, f audio.Format) (audio.Output, error)
}

// RouterFunc — Router из функции.
type RouterFunc func(c Class, f audio.Format) (audio.Output, error)

func (r RouterFunc) Output(c Class, f audio.Format) (audio.Output, error) { return r(c, f) }

// NoSpectrum — поток без спектра (Options.Spectrum).
const NoSpectrum = -1

// Options — свойства потока.
type Options struct {
	Gain          float64       // постоянная громкость потока (нормализация трека), множитель; 0 — 1
	FadeIn        time.Duration // нарастание в начале каждого вывода (старт и продолжение после паузы)
	Spectrum      int           // spectrum.SourceMusic или spectrum.SourceVoice; NoSpectrum — не считать
	RewindOnPause bool          // при паузе вернуть источнику то, что записано в вывод, но не прозвучало (нужен Rewinder)
	PosLatency    time.Duration // через сколько после записи звук слышен: вычитается из позиции по часам
	Endless       bool          // источник не кончается (звонок по кругу): позиция не ограничивается длительностью из Info
}

// Events — что поток сообщает. Колбэки вызываются без удерживаемых блокировок движка, из его горутин.
type Events struct {
	Ready   func(audio.Info) // источник открыт и готов отдавать звук
	Playing func()           // первый звук потока ушёл в вывод (один раз за жизнь потока)
	Ended   func()           // звук кончился и доигран (или оборвался после того, как уже звучал)
	Failed  func(err error)  // не получилось до первого звука
}

// Engine — общие настройки и громкости классов. Нулевое значение (с Router) готово к работе.
type Engine struct {
	Router Router
	// Now — часы для позиции и возврата звука (по умолчанию time.Now); подменяются в тестах.
	Now  func() time.Time
	Logf func(string, ...any)
	// OnSpectrum (необязательный) получает кадры спектра и смены состояния его потоков. Вызывается из цикла вывода:
	// обработчик должен быстро возвращаться и копировать кадр, если хранит его.
	OnSpectrum func(spectrum.Event)

	gains [numClasses]atomic.Uint64 // громкость класса (биты float64); 0 — ещё не задана, то есть 1

	mu   sync.Mutex
	live map[*Stream]struct{} // потоки до Close (для Reroute)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(f string, a ...any) {
	if e.Logf != nil {
		e.Logf(f, a...)
	}
}

// SetClassGain задаёт громкость класса (1 — как есть; меньше — приглушение). Звучащие потоки переходят к ней плавно.
func (e *Engine) SetClassGain(c Class, g float64) {
	if c >= 0 && c < numClasses {
		e.gains[c].Store(math.Float64bits(g))
	}
}

// ClassGain — текущая громкость класса.
func (e *Engine) ClassGain(c Class) float64 {
	if c < 0 || c >= numClasses {
		return 1
	}
	b := e.gains[c].Load()
	if b == 0 {
		return 1
	}
	return math.Float64frombits(b)
}

// NewStream создаёт поток. id нужен только для журнала. Источник ещё не открыт: см. Stream.Open.
func (e *Engine) NewStream(id string, c Class, src Source, opt Options, ev Events) *Stream {
	if opt.Gain == 0 {
		opt.Gain = 1
	}
	if e.Router == nil {
		panic("engine: не задан Router")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stream{e: e, id: id, class: c, src: src, opt: opt, ev: ev, f: src.Format(), ctx: ctx, cancel: cancel,
		readyCh: make(chan struct{}), wake: make(chan struct{})}
	if s.f.Rate == 0 || s.f.Channels == 0 {
		panic(fmt.Sprintf("engine: источник %s без формата", id))
	}
	e.mu.Lock()
	if e.live == nil {
		e.live = map[*Stream]struct{}{}
	}
	e.live[s] = struct{}{}
	e.mu.Unlock()
	return s
}

func (e *Engine) forget(s *Stream) {
	e.mu.Lock()
	delete(e.live, s)
	e.mu.Unlock()
}

// Reroute заново выбирает выход играющим потокам тех классов, для которых match вернул true: появился выход лучше
// нынешнего (подключились наушники — музыка уходит в них посреди трека). Возвращает, сколько потоков переведено.
func (e *Engine) Reroute(match func(Class) bool) int {
	e.mu.Lock()
	var list []*Stream
	for s := range e.live {
		if match(s.class) {
			list = append(list, s)
		}
	}
	e.mu.Unlock()
	n := 0
	for _, s := range list {
		if s.Reroute() {
			n++
		}
	}
	return n
}
