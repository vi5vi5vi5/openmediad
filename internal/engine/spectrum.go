package engine

import (
	"time"

	"openmediad/internal/spectrum"
	"openmediad/internal/system/clock"
)

// Спектр потока для дисплея и подсветки (как у штатного mediad). Считается по тому же звуку, что уходит в вывод (после
// нормализации и приглушения). У потока один идентификатор спектра на всю жизнь: первый вывод — «старт», продолжение после
// паузы — «продолжение», конец каждого вывода — «остановка».

func (r *loopRun) spectrumOn() bool {
	return r.s.e.OnSpectrum != nil && r.s.opt.Spectrum != NoSpectrum
}

// startSpectrum готовит анализатор цикла. Служебное сообщение о старте уходит с первым звуком (или с первым отводом
// источника), а не сразу: цикл, который так и не дал звука, поток спектра не открывает.
func (r *loopRun) startSpectrum() {
	if !r.spectrumOn() {
		return
	}
	s := r.s
	r.an = spectrum.New(func(db []float32) {
		s.e.OnSpectrum(spectrum.Event{Kind: spectrum.KindData, Source: s.opt.Spectrum, Stream: r.specID(), DB: db, At: time.Now()})
	})
	if t, ok := s.src.(SpectrumTapper); ok {
		t.SetSpectrumTap(func(pcm []byte) { r.feed(pcm) })
	}
}

func (r *loopRun) specID() uint64 {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.specID
}

// announce отправляет «старт» (первый вывод потока) или «продолжение» один раз за цикл.
func (r *loopRun) announce() {
	if r.specStart {
		return
	}
	r.specStart = true
	s := r.s
	s.mu.Lock()
	kind := spectrum.KindResume
	if s.specID == 0 {
		s.specID = uint64(clock.Mono())
		kind = spectrum.KindStart
	}
	id := s.specID
	s.mu.Unlock()
	s.e.OnSpectrum(spectrum.Event{Kind: kind, Source: s.opt.Spectrum, Stream: id, At: time.Now()})
}

// feed отдаёт звук анализатору.
func (r *loopRun) feed(pcm []byte) {
	if r.an == nil {
		return
	}
	r.announce()
	if r.s.f.Channels == 1 {
		r.an.WriteMono(pcm)
	} else {
		r.an.Write(pcm)
	}
}

func (r *loopRun) spectrum(pcm []byte) { r.feed(pcm) }

// stopSpectrum закрывает поток спектра цикла (если он открывался). Повторный вызов ничего не делает.
func (r *loopRun) stopSpectrum() {
	if r.an == nil {
		return
	}
	s := r.s
	if t, ok := s.src.(SpectrumTapper); ok {
		t.SetSpectrumTap(nil)
	}
	if r.specStart {
		s.e.OnSpectrum(spectrum.Event{Kind: spectrum.KindStop, Source: s.opt.Spectrum, Stream: r.specID(), At: time.Now()})
	}
	r.an = nil
}
