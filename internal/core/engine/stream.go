package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/core/audio"
)

// Stream — поток: источник, класс, вывод, состояние. Методы быстрые и не блокируются на сети и процессах: всё долгое идёт
// в горутинах потока.
type Stream struct {
	e     *Engine
	id    string
	class Class
	src   Source
	opt   Options
	ev    Events
	f     audio.Format

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	openCalled bool
	opened     bool // источник открыт без ошибки
	openErr    error
	info       audio.Info
	readyCh    chan struct{} // закрывается, когда открытие закончилось (успехом или ошибкой)
	playing    bool
	closed     bool
	gen        int           // поколение вывода: Play, Pause и Close его меняют, и старый цикл вывода останавливается
	wake       chan struct{} // закрывается при смене поколения: будит цикл, который ждёт открытия источника
	loopDone   chan struct{} // закрывается, когда последний запущенный цикл вывода завершился
	out        audio.Output  // вывод текущего цикла (пауза обрывает его сразу)
	seekGen    int           // меняется при перемотке: звук, прочитанный до неё, в учёт позиции не идёт
	base       time.Duration // позиция первого отсчёта после открытия или перемотки
	specID     uint64        // идентификатор потока спектра (0 — ещё не начат)

	played    atomic.Uint64 // байт, переданных выводу после base
	outAt     atomic.Int64  // Unix-наносекунды первой записи текущего цикла (0 — сейчас не звучит)
	outPlayed atomic.Uint64 // played на момент первой записи текущего цикла
	sounded   atomic.Bool   // первый звук потока уже ушёл (Playing отправлено)
	finished  atomic.Bool   // Ended или Failed уже отправлены
}

// ID — имя потока для журнала.
func (s *Stream) ID() string { return s.id }

// Class — класс потока.
func (s *Stream) Class() Class { return s.class }

// Source — источник потока.
func (s *Stream) Source() Source { return s.src }

// bump меняет поколение вывода и будит ждущий цикл. Вызывать под s.mu.
func (s *Stream) bump() {
	s.gen++
	close(s.wake)
	s.wake = make(chan struct{})
}

// Open начинает открывать источник в фоне. Повторный вызов ничего не делает.
func (s *Stream) Open() {
	s.mu.Lock()
	if s.openCalled || s.closed {
		s.mu.Unlock()
		return
	}
	s.openCalled = true
	s.mu.Unlock()
	go func() {
		info, err := s.src.Open(s.ctx)
		s.mu.Lock()
		closed := s.closed
		if err == nil {
			s.opened, s.info, s.base = true, info, info.Start
		} else {
			s.openErr = err
		}
		s.mu.Unlock()
		switch {
		case closed:
		case err != nil:
			s.fail(err)
		case s.ev.Ready != nil:
			s.ev.Ready(info)
		}
		// цикл вывода ждёт readyCh: закрыть его только после Ready, иначе первый звук (Playing) мог бы обогнать Ready
		close(s.readyCh)
	}()
}

// Playing — идёт ли вывод (Play без Pause, звук ещё не кончился).
func (s *Stream) Playing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playing
}

// Ready — открыт ли источник и что о нём известно.
func (s *Stream) Ready() (audio.Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info, s.opened
}

// Play начинает или продолжает вывод. Вывод открывается сразу (так первый звук не ждёт запуска aplay), а звук пойдёт,
// когда источник будет готов. Без Open источник откроется сам.
func (s *Stream) Play() {
	s.Open()
	s.mu.Lock()
	if s.closed || s.playing {
		s.mu.Unlock()
		return
	}
	s.playing = true
	s.bump()
	gen, prev := s.gen, s.loopDone
	done := make(chan struct{})
	s.loopDone = done
	s.mu.Unlock()
	go s.loop(gen, prev, done)
}

// Reroute перезапускает вывод играющего потока: новый цикл заново выбирает выход маршрута. Как пауза и продолжение
// подряд, только без событий: не прозвучавшее в старом выводе возвращается источнику (Options.RewindOnPause), и звук
// продолжается в новом выходе с того места, где старый замолчал. false — поток не играет.
func (s *Stream) Reroute() bool {
	s.mu.Lock()
	if s.closed || !s.playing {
		s.mu.Unlock()
		return false
	}
	s.bump()
	gen, prev, out := s.gen, s.loopDone, s.out
	done := make(chan struct{})
	s.loopDone = done
	s.mu.Unlock()
	s.interrupt(out)
	s.e.logf("поток %s: выход маршрута %s выбирается заново (появился выход лучше)", s.id, s.class)
	go s.loop(gen, prev, done)
	return true
}

// Pause останавливает вывод сразу; источник, буфер и позиция остаются. Звук, записанный в вывод, но не прозвучавший,
// возвращается источнику (Options.RewindOnPause), и «продолжи» идёт с того места, где динамик замолчал.
func (s *Stream) Pause() {
	s.mu.Lock()
	if s.closed || !s.playing {
		s.mu.Unlock()
		return
	}
	s.playing = false
	s.bump()
	out := s.out
	s.mu.Unlock()
	s.interrupt(out)
}

// Close освобождает всё: вывод обрывается, источник закрывается. Событий после Close не бывает.
func (s *Stream) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.playing = true, false
	s.bump()
	out := s.out
	s.mu.Unlock()
	s.e.forget(s)
	s.cancel()
	s.interrupt(out)
	s.src.Close()
}

// CloseWait — Close с ожиданием: возвращается, когда вывод действительно остановлен (aplay завершился). Для остановки
// демона: процессы вывода не должны пережить его.
func (s *Stream) CloseWait() {
	s.Close()
	s.mu.Lock()
	done := s.loopDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// interrupt будит цикл вывода, где бы он ни ждал: в чтении источника (Waker) или в записи в вывод (обрыв вывода).
// Обрыв aplay ждёт его завершения, поэтому идёт в фоне: вызывающий (разбор команды шины) задерживаться не должен.
func (s *Stream) interrupt(out audio.Output) {
	if w, ok := s.src.(Waker); ok {
		w.Wake()
	}
	if out != nil {
		go out.Abort()
	}
}

// Seek переходит к позиции: источник перематывается (если умеет), позиция потока начинается с нового места.
func (s *Stream) Seek(pos time.Duration) {
	sk, ok := s.src.(Seeker)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.closed || !s.opened {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	actual, err := sk.Seek(pos)
	if err != nil {
		s.e.logf("поток %s: перемотка на %s: %v", s.id, pos, err)
		return
	}
	s.mu.Lock()
	s.seekGen++
	s.base = actual
	s.played.Store(0)
	s.outPlayed.Store(0)
	if s.outAt.Load() != 0 {
		s.outAt.Store(s.e.now().UnixNano())
	}
	s.mu.Unlock()
}

// Pos — позиция воспроизведения. Записанное в вывод опережает динамик на размер буферов (труба aplay, смеситель), поэтому
// позиция — меньшее из «записано» и «прошло времени с первой записи» (минус задержку до звука): так прогресс идёт по
// часам, как у mediad, а не убегает вперёд.
func (s *Stream) Pos() time.Duration {
	s.mu.Lock()
	base, dur := s.base, s.info.Duration
	s.mu.Unlock()
	played := s.played.Load()
	if at := s.outAt.Load(); at != 0 {
		el := s.e.now().Sub(time.Unix(0, at)) - s.opt.PosLatency
		if el < 0 {
			el = 0
		}
		if byClock := s.outPlayed.Load() + uint64(s.f.Bytes(el)); byClock < played {
			played = byClock
		}
	}
	pos := base + s.f.Duration(played)
	if dur > 0 && pos > dur && !s.opt.Endless {
		pos = dur
	}
	return pos
}

// Elapsed — сколько звука передано выводу с открытия или последней перемотки (по часам, как Pos, но без base).
func (s *Stream) Elapsed() time.Duration {
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	return s.Pos() - base
}

// fail сообщает об ошибке: до первого звука — Failed, после — Ended (звучавший поток считается законченным). Один раз.
func (s *Stream) fail(err error) {
	if !s.finished.CompareAndSwap(false, true) {
		return
	}
	if s.sounded.Load() {
		s.e.logf("поток %s: %v — считаю законченным", s.id, err)
		if s.ev.Ended != nil {
			s.ev.Ended()
		}
		return
	}
	if s.ev.Failed != nil {
		s.ev.Failed(err)
	}
}

// end сообщает о конце звука. Один раз.
func (s *Stream) end() {
	if !s.finished.CompareAndSwap(false, true) {
		return
	}
	if s.ev.Ended != nil {
		s.ev.Ended()
	}
}
