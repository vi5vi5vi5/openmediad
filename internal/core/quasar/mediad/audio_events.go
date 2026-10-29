package mediad

// Плеер музыки: события по таймерам (STARTING, PLAYING, progress, flag, PAUSED), ответы бэкенда (Opened, Failed, Finished)
// и сборка события из состояния плеера.

import (
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
)

// starting — через startingDelay после play: STARTING, затем PLAYING (сразу, если источник уже готов).
func (m *Machine) starting(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stStarting {
		m.mu.Unlock()
		return
	}
	in.started = true
	ev := m.event(in, 1, 1, in.offset)
	ready := in.opened
	m.mu.Unlock()
	m.Emit(ev)
	m.Back.Start(id)
	if ready {
		m.schedule(&in.t, nil, warmPlaying, func() { m.playing(id) })
	}
}

// Opened — бэкенд открыл поток: известна длительность, можно играть.
func (m *Machine) Opened(id string, durMs uint64) {
	m.mu.Lock()
	m.init()
	in := m.players[id]
	if in == nil {
		m.mu.Unlock()
		return
	}
	in.opened, in.durMs = true, durMs
	wait := in.st == stStarting && in.started
	m.mu.Unlock()
	if wait {
		m.playing(id)
	}
}

// Failed — бэкенд не смог открыть поток. До play это запоминается (play уйдёт настоящему mediad), после play
// команда пересылается mediad сразу, а наш плеер снимается.
func (m *Machine) Failed(id string, err error) {
	m.mu.Lock()
	m.init()
	in := m.players[id]
	if in == nil {
		m.mu.Unlock()
		return
	}
	m.logf("плеер %s: поток не открылся: %v", id, err)
	m.note("failed", id, err.Error())
	in.failed = true
	if in.st != stStarting && in.st != stPlaying {
		m.mu.Unlock()
		return
	}
	in.t.stop()
	raw, ctx := in.raw, in.ctx
	delete(m.players, id)
	m.foreign[id] = true
	m.mu.Unlock()
	m.Back.Close(id)
	m.forward(ctx, raw)
}

func (m *Machine) playing(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stStarting || !in.started {
		m.mu.Unlock()
		return
	}
	in.st = stPlaying
	pos := m.Back.Pos(id)
	if pos < in.offset {
		pos = in.offset
	}
	st := m.event(in, 1, 3, pos)
	pr := m.event(in, 2, 3, pos)
	in.seenProg = true
	m.mu.Unlock()
	m.Emit(st)
	m.Emit(pr)
	m.schedule(&in.t, nil, flagDelay, func() { m.flagEvent(id) })
	m.schedule(&in.t, nil, progressEvery, func() { m.progress(id) })
}

func (m *Machine) progress(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPlaying {
		m.mu.Unlock()
		return
	}
	ev := m.event(in, 2, 3, m.Back.Pos(id))
	m.mu.Unlock()
	m.Emit(ev)
	m.schedule(&in.t, nil, progressEvery, func() { m.progress(id) })
}

func (m *Machine) flagEvent(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPlaying {
		m.mu.Unlock()
		return
	}
	ev := m.event(in, 6, 3, m.Back.Pos(id))
	m.mu.Unlock()
	m.Emit(ev)
}

func (m *Machine) paused(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPausing {
		m.mu.Unlock()
		return
	}
	in.st = stPaused
	in.stopMs = uint64(m.Now().UnixMilli())
	m.byTrack[in.meta.Player.Stream] = id
	ev := m.event(in, 1, 4, m.Back.Pos(id))
	evict := m.evictPaused("", id) // на паузе держим один плеер: самый свежий
	m.mu.Unlock()
	m.Emit(ev)
	for _, eid := range evict {
		m.Back.Close(eid)
	}
	m.schedule(&in.t, nil, pauseCache, func() { m.expire(id) })
}

// expire закрывает плеер, простоявший на паузе дольше pauseCache.
func (m *Machine) expire(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPaused {
		m.mu.Unlock()
		return
	}
	if m.byTrack[in.meta.Player.Stream] == id {
		delete(m.byTrack, in.meta.Player.Stream)
	}
	delete(m.players, id)
	m.note("expire", id, "30 минут на паузе")
	m.mu.Unlock()
	m.Back.Close(id)
}

// Finished — трек доигран до конца: ENDED с позицией, равной длительности.
func (m *Machine) Finished(id string) {
	m.mu.Lock()
	in := m.players[id]
	if in == nil || in.st != stPlaying {
		m.mu.Unlock()
		return
	}
	in.t.stop()
	in.st = stEnded
	in.stopMs = uint64(m.Now().UnixMilli())
	ev := m.event(in, 1, 5, in.durMs)
	delete(m.players, id)
	m.note("finished", id, "трек доигран до конца")
	m.mu.Unlock()
	m.Emit(ev)
	m.Back.Close(id)
}

// event собирает событие плеера. Вызывать под m.mu.
func (m *Machine) event(in *inst, kind, st, posMs uint64) []byte {
	s := Snap{
		Kind: msg.EventKind(kind), State: msg.PlayState(st),
		PosMs: posMs, Idx: posMs / 1000, UUID: newUUID(),
		Vsid: in.vsid, Start: in.startMs, Stop: in.stopMs,
		Over: in.over,
	}
	if in.durMs > 0 {
		s.HasDur, s.DurS = true, (in.durMs+500)/1000
		s.Mono = m.Mono()
	}
	switch kind {
	case 2:
		s.HasT9, s.T9 = true, in.seenProg
		in.seenProg = true
	case 6:
		s.HasT17, s.T17 = true, !m.ducked
	}
	got, total := m.Back.Net(in.meta.Player.ID)
	s.Diag = Diag(in.mark, in.vsid, got, total, st == 5, posMs/1000)
	return in.meta.Event(s)
}

func setOver(old []pb.Field, add ...pb.Field) []pb.Field {
	var out []pb.Field
	for _, f := range old {
		keep := true
		for _, a := range add {
			if a.Num == f.Num {
				keep = false
			}
		}
		if keep {
			out = append(out, f)
		}
	}
	return append(out, add...)
}

// descURLFields — поля 2 и 42 дескриптора открытого плеера (без суффикса vsid): их наследует возобновлённый плеер.
func descURLFields(desc []byte, prev []pb.Field) []pb.Field {
	out := append([]pb.Field(nil), prev...)
	var u2, u42 []pb.Field
	r := pb.NewReader(desc)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		switch f.Num {
		case 2:
			u2 = append(u2, f)
		case 42:
			u42 = append(u42, f)
		}
	}
	if len(u2) > 0 {
		out = setOver(out, u2...)
	}
	if len(u42) > 0 {
		out = setOver(out, u42...)
	}
	return out
}
