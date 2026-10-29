package engine

import (
	"errors"
	"fmt"
	"io"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/spectrum"
)

// chunk — порция вывода: 50 мс звука. Между порциями проверяются пауза, перемотка и громкость.
const chunk = 50 * time.Millisecond

// Переезд на другой выход, когда вывод отказал посреди звука (отключились наушники): не больше maxReroutes подряд,
// с паузой rerouteWait перед каждым (успевают прийти сигналы BlueZ, и маршрутизатор не выберет пропавший выход снова).
// Счётчик сбрасывается после rerouteReset спокойной работы.
const (
	maxReroutes  = 3
	rerouteWait  = 300 * time.Millisecond
	rerouteReset = 10 * time.Second
)

// loopRun — состояние одного цикла вывода (от Play до паузы, конца или закрытия).
type loopRun struct {
	s       *Stream
	gen     int
	out     audio.Output
	drained bool // вывод доигран и закрыт (иначе при выходе он обрывается)

	firstAt     time.Time // первая запись в вывод (нулевое — ещё не было)
	startPlayed uint64    // played на момент первой записи
	startSeek   int       // seekGen на старте цикла: перемотка во время цикла отменяет возврат звука при паузе

	gain float64 // текущая громкость (с плавным переходом к цели)
	fade int     // сколько байт нарастания осталось

	an        *spectrum.Analyzer
	specStart bool      // служебное сообщение о старте/продолжении потока спектра уже ушло
	specAt    time.Time // когда поток спектра объявлен последний раз (SpectrumAlways: объявлять снова)

	reroutes  int       // переездов на другой выход подряд
	rerouteAt time.Time // последний переезд
}

// loop гонит звук источника в вывод, пока не сменится поколение (пауза, закрытие). В конце звука доигрывает остаток
// и сообщает Ended; при остановке обрывает вывод сразу.
func (s *Stream) loop(gen int, prev, done chan struct{}) {
	defer close(done)
	if prev != nil {
		<-prev // прежний цикл должен успеть вернуть непрозвучавшее источнику, прежде чем этот начнёт читать
	}
	if s.stopped(gen) {
		return
	}
	out, err := s.e.Router.Output(s.class, s.f)
	if err != nil {
		if !s.stopped(gen) {
			s.fail(fmt.Errorf("запуск вывода %s: %w", s.class, err))
		}
		return
	}
	r := &loopRun{s: s, gen: gen, out: out, gain: s.e.ClassGain(s.class) * s.opt.Gain}
	s.mu.Lock()
	if s.gen != gen || s.closed {
		s.mu.Unlock()
		out.Abort()
		return
	}
	s.out = out
	r.startSeek = s.seekGen
	wake := s.wake
	s.mu.Unlock()
	defer r.finish()

	select { // источник ещё открывается (холодный старт): ждём его или остановки
	case <-s.readyCh:
	case <-wake:
		return
	}
	s.mu.Lock()
	openErr := s.openErr
	s.mu.Unlock()
	if openErr != nil {
		return // об ошибке открытия уже сообщено
	}
	if s.opt.FadeIn > 0 {
		r.fade = s.f.Bytes(s.opt.FadeIn)
	}
	r.startSpectrum()
	buf := make([]byte, s.f.Bytes(chunk))
	for r.step(buf) {
	}
}

// stopped — цикл поколения gen должен остановиться.
func (s *Stream) stopped(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen != gen || s.closed
}

// step выводит одну порцию; false — цикл закончен.
func (r *loopRun) step(buf []byte) bool {
	s := r.s
	s.mu.Lock()
	if s.gen != r.gen || s.closed {
		s.mu.Unlock()
		return false
	}
	sg := s.seekGen
	s.mu.Unlock()

	n, err := s.src.Read(buf)
	switch {
	case errors.Is(err, audio.ErrWake):
		return true
	case errors.Is(err, audio.ErrStopped):
		if s.stopped(r.gen) {
			return false
		}
		time.Sleep(10 * time.Millisecond) // источник меняет буфер (перемотка): данные скоро будут
		return true
	case errors.Is(err, io.EOF):
		if s.stopped(r.gen) {
			return false
		}
		r.out.Drain()
		r.drained = true
		r.stopSpectrum()
		s.mu.Lock()
		s.playing = false
		s.mu.Unlock()
		s.end()
		return false
	case err != nil:
		if !s.stopped(r.gen) {
			s.fail(fmt.Errorf("источник: %w", err))
		}
		return false
	}
	s.mu.Lock()
	stale, stop := s.seekGen != sg, s.gen != r.gen || s.closed
	s.mu.Unlock()
	if stop {
		return false
	}
	if stale || n == 0 { // прочитано до перемотки: этого звука уже не должно быть
		return true
	}
	p := buf[:n]
	if r.fade > 0 {
		r.fade = audio.FadeIn(p, r.fade, s.f.Bytes(s.opt.FadeIn))
	}
	r.gain = audio.ApplyGain(p, r.gain, s.e.ClassGain(s.class)*s.opt.Gain)
	r.spectrum(p)
	if _, err := r.out.Write(p); err != nil {
		if s.stopped(r.gen) {
			return false
		}
		if !r.reroute(err, p) {
			s.fail(fmt.Errorf("запись в вывод %s: %w", s.class, err))
			return false
		}
	} else if r.reroutes > 0 && s.e.now().Sub(r.rerouteAt) > rerouteReset {
		r.reroutes = 0
	}
	s.mu.Lock()
	if s.seekGen == sg {
		if r.firstAt.IsZero() {
			r.firstAt = s.e.now()
			r.startPlayed = s.played.Load()
			s.outPlayed.Store(r.startPlayed)
			s.outAt.Store(r.firstAt.UnixNano())
		}
		s.played.Add(uint64(n))
	}
	s.mu.Unlock()
	if s.sounded.CompareAndSwap(false, true) && s.ev.Playing != nil {
		s.ev.Playing()
	}
	return true
}

// reroute переводит поток на другой выход маршрута, когда текущий отказал (наушники отключились посреди звука), и пишет
// в него порцию p. Звук, лежавший в буферах отказавшего вывода, пропадает (доли секунды); позиция по часам начинается
// заново с уже записанного. false — переехать не вышло (поток останавливается или выходов нет).
func (r *loopRun) reroute(cause error, p []byte) bool {
	s := r.s
	for r.reroutes < maxReroutes {
		r.reroutes++
		r.rerouteAt = s.e.now()
		r.out.Abort()
		time.Sleep(rerouteWait)
		if s.stopped(r.gen) {
			return false
		}
		out, err := s.e.Router.Output(s.class, s.f)
		if err != nil {
			s.e.logf("поток %s: вывод отказал (%v), другого выхода нет: %v", s.id, cause, err)
			return false
		}
		s.mu.Lock()
		if s.gen != r.gen || s.closed {
			s.mu.Unlock()
			out.Abort()
			return false
		}
		s.out = out
		s.mu.Unlock()
		r.out = out
		r.firstAt = time.Time{} // позиция по часам — от первой записи в новый вывод
		s.outAt.Store(0)
		s.e.logf("поток %s: вывод отказал (%v) — звук переведён на другой выход маршрута %s", s.id, cause, s.class)
		_, err = out.Write(p)
		if err == nil {
			return true
		}
		cause = err
	}
	return false
}

// finish — выход из цикла: оборвать вывод (если не доигран), вернуть непрозвучавшее при паузе, закрыть поток спектра.
func (r *loopRun) finish() {
	s := r.s
	if !r.drained {
		r.out.Abort()
		r.rewind()
	}
	s.mu.Lock()
	if s.out == r.out {
		s.out = nil
	}
	s.mu.Unlock()
	s.outAt.Store(0)
	r.stopSpectrum()
}

// rewind возвращает источнику звук, который записан в вывод, но до динамика не дошёл: труба и буферы aplay и смесителя
// держат до ≈0,5 с, а обрыв их выбрасывает. Только при паузе (поток жив, перемотки за цикл не было).
func (r *loopRun) rewind() {
	s := r.s
	rw, ok := s.src.(Rewinder)
	if !ok || !s.opt.RewindOnPause || r.firstAt.IsZero() {
		return
	}
	s.mu.Lock()
	pause := !s.closed && s.gen != r.gen && s.seekGen == r.startSeek
	s.mu.Unlock()
	if !pause {
		return
	}
	written := s.played.Load() - r.startPlayed
	audible := uint64(0)
	if el := s.e.now().Sub(r.firstAt) - s.opt.PosLatency; el > 0 {
		audible = uint64(s.f.Bytes(el))
	}
	if written <= audible {
		return
	}
	fb := uint64(s.f.FrameBytes())
	lost := int((written - audible) / fb * fb) // целое число отсчётов всех каналов
	if k := rw.Rewind(lost); k > 0 {
		s.played.Add(^uint64(k) + 1) // played -= k
		s.e.logf("поток %s: пауза: возвращено %s звука, не дошедшего до динамика", s.id, s.f.Duration(uint64(k)).Round(time.Millisecond))
	}
}
