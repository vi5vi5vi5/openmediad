package engine

import (
	"time"

	"openmediad/internal/core/platform/clock"
	"openmediad/internal/core/spectrum"
)

// Спектр потока для дисплея и подсветки (как у штатного mediad). Считается по тому же звуку, что уходит в вывод (после
// нормализации и приглушения). У потока один идентификатор спектра на всю жизнь: первый вывод — «старт», продолжение после
// паузы — «продолжение», конец каждого вывода — «остановка».

// spectrumRefresh — как часто объявлять спектр музыки заново (SpectrumAlways): платформа гасит анимацию через 15 с после
// «начат» («Stop spectogram by timeout», проверено 28.10.2026), а каждое «начат» отсчёт сбрасывает.
var spectrumRefresh = 10 * time.Second

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
	if s.opt.Spectrum == spectrum.SourceVoice {
		s.e.voiceSpectra.Add(1)
	}
	r.specAt = s.e.now()
	s.e.OnSpectrum(spectrum.Event{Kind: kind, Source: s.opt.Spectrum, Stream: id, At: time.Now()})
}

// refreshSpectrum объявляет спектр музыки заново, если пора (SpectrumAlways): не во время диалога (музыка приглушена) и не
// пока звучит голос — их анимацию не перебиваем.
func (r *loopRun) refreshSpectrum() {
	s := r.s
	e := s.e
	if !e.SpectrumAlways || s.opt.Spectrum != spectrum.SourceMusic || !r.specStart {
		return
	}
	now := e.now()
	if now.Sub(r.specAt) < spectrumRefresh || e.ClassGain(Music) < 1 || e.voiceSpectra.Load() > 0 {
		return
	}
	r.specAt = now
	e.OnSpectrum(spectrum.Event{Kind: spectrum.KindStart, Source: spectrum.SourceMusic, Stream: r.specID(), At: time.Now()})
}

// feed отдаёт звук анализатору.
func (r *loopRun) feed(pcm []byte) {
	if r.an == nil {
		return
	}
	r.announce()
	r.refreshSpectrum()
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
		if s.opt.Spectrum == spectrum.SourceVoice { // Алиса договорила: вернуть платформе спектр музыки
			s.e.voiceSpectra.Add(-1)
			s.e.reannounceMusic()
		}
	}
	r.an = nil
}
