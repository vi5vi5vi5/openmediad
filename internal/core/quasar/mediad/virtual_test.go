package mediad

import (
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/quasar/msg"
)

func vtrack(playing bool) *Virtual {
	return &Virtual{Track: "spotify:track:1", Title: "Песня", Artist: "Группа", DurMs: 161000, PosMs: 5000, Playing: playing}
}

func TestVirtualStartTellsPlatformTheTrackIsPlaying(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	st := r.states()
	if len(st) != 2 || !strings.HasPrefix(st[0], "STARTING/") || !strings.HasPrefix(st[1], "PLAYING/") {
		t.Fatalf("события: %v", st)
	}
	id := r.events()[0].player()
	if id == "" || strings.TrimPrefix(st[1], "PLAYING/") != id {
		t.Fatalf("плеер события %q и %q", id, st[1])
	}
	if got := msg.AudioEvent(r.events()[0].body).TrackID(); got != "spotify:track:1" {
		t.Fatalf("описание трека в событии: %q", got)
	}
	if pl := msg.AudioEvent(r.events()[0].body).Player(); pl.Kind != msg.PlayerAudio || pl.Stream != "spotify:track:1" {
		t.Fatalf("дескриптор плеера: %+v", pl)
	}
	// дальше идёт прогресс раз в секунду, позиция растёт со временем
	r.advance(3 * time.Second)
	var prog []uint64
	for _, g := range r.events() {
		if g.kind() == 2 {
			prog = append(prog, g.pos())
		}
	}
	if len(prog) < 3 || prog[len(prog)-1] < 7000 || prog[len(prog)-1] > 9000 {
		t.Fatalf("прогресс: %v (ждали рост с 5000 примерно до 8000)", prog)
	}
}

func TestVirtualSameStateSendsNothing(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	n := len(r.events())
	r.m.SetVirtual(vtrack(true))
	r.m.SetVirtual(vtrack(true))
	if len(r.events()) != n {
		t.Fatalf("повторный вызов с тем же состоянием породил события: было %d, стало %d", n, len(r.events()))
	}
}

func TestVirtualPauseAndResume(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.advance(2 * time.Second)
	r.m.SetVirtual(&Virtual{Track: "spotify:track:1", Title: "Песня", Artist: "Группа", DurMs: 161000, PosMs: 7000, Playing: false})
	st := r.states()
	if last := st[len(st)-1]; !strings.HasPrefix(last, "PAUSED/") {
		t.Fatalf("после паузы события %v", st)
	}
	n := len(r.events())
	r.advance(5 * time.Second)
	if len(r.events()) != n {
		t.Fatal("на паузе виртуальный плеер продолжает слать прогресс")
	}
	r.m.SetVirtual(&Virtual{Track: "spotify:track:1", Title: "Песня", Artist: "Группа", DurMs: 161000, PosMs: 7000, Playing: true})
	st = r.states()
	if last := st[len(st)-1]; !strings.HasPrefix(last, "PLAYING/") {
		t.Fatalf("после возобновления события %v", st)
	}
	ids := map[string]bool{}
	for _, g := range r.events() {
		ids[g.player()] = true
	}
	if len(ids) != 1 {
		t.Fatalf("пауза и продолжение должны идти тем же плеером: %v", ids)
	}
	r.advance(2 * time.Second)
	if evs := r.events(); evs[len(evs)-1].pos() < 8000 {
		t.Fatalf("прогресс не возобновился: позиция %d", evs[len(evs)-1].pos())
	}
}

func TestVirtualNewTrackEndsOldPlayer(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	first := r.events()[0].player()
	r.advance(time.Second)
	next := vtrack(true)
	next.Track, next.Title, next.PosMs = "spotify:track:2", "Другая", 0
	r.m.SetVirtual(next)
	st := r.states()
	if len(st) != 5 || st[2] != "ENDED/"+first || !strings.HasPrefix(st[3], "STARTING/") || !strings.HasPrefix(st[4], "PLAYING/") {
		t.Fatalf("смена трека: %v", st)
	}
	if second := strings.TrimPrefix(st[4], "PLAYING/"); second == first {
		t.Fatal("у нового трека тот же плеер")
	}
	// старый плеер молчит: прогресс идёт только у нового
	r.advance(3 * time.Second)
	for _, g := range r.events()[5:] {
		if g.player() == first {
			t.Fatal("событие после ENDED у старого плеера")
		}
	}
}

func TestVirtualStopEndsPlayerOnce(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.m.SetVirtual(nil)
	r.m.SetVirtual(nil)
	st := r.states()
	if len(st) != 3 || !strings.HasPrefix(st[2], "ENDED/") {
		t.Fatalf("остановка: %v", st)
	}
	n := len(r.events())
	r.advance(5 * time.Second)
	if len(r.events()) != n {
		t.Fatal("после остановки идут события")
	}
}

func TestVirtualStartsPausedWithoutPlaying(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(false))
	st := r.states()
	if len(st) != 2 || !strings.HasPrefix(st[1], "PAUSED/") {
		t.Fatalf("сессия на паузе: %v", st)
	}
}

func TestVirtualSeekIsReportedImmediately(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.advance(time.Second)
	n := len(r.events())
	jump := vtrack(true)
	jump.PosMs = 90000
	r.m.SetVirtual(jump)
	evs := r.events()
	if len(evs) != n+1 || evs[n].kind() != 2 || evs[n].pos() != 90000 {
		t.Fatalf("перемотка: события после неё %d, последнее kind=%v pos=%d", len(evs)-n, evs[len(evs)-1].kind(), evs[len(evs)-1].pos())
	}
	small := vtrack(true)
	small.PosMs = 91500 // отклонение в пределах порога: не перемотка
	r.m.SetVirtual(small)
	if len(r.events()) != n+1 {
		t.Fatal("небольшое расхождение позиции принято за перемотку")
	}
}

func TestVirtualPositionUsesWhenItWasKnown(t *testing.T) {
	r := newRig(t)
	r.advance(10 * time.Second)
	v := vtrack(true)
	v.PosMs = 5000
	v.PosAt = r.m.Now().Add(-4 * time.Second) // позиция получена 4 с назад: играет уже с 9000
	r.m.SetVirtual(v)
	if p := r.events()[0].pos(); p < 8000 {
		t.Fatalf("позиция %d: время с момента PosAt не учтено", p)
	}
	for _, g := range r.events() {
		if g.kind() == 1 && g.state() == msg.PlayState(3) && g.pos() < 8900 {
			t.Fatalf("PLAYING с позицией %d", g.pos())
		}
	}
}

func TestPlatformCommandsToVirtualPlayerGoToTheSource(t *testing.T) {
	r := newRig(t)
	var cmds []string
	r.m.OnVirtualCommand(func(c string) { cmds = append(cmds, c) })
	r.m.SetVirtual(vtrack(true))
	id := r.events()[0].player()
	if !r.m.Handle(nil, pauseCmd(id, "spotify:track:1")) {
		t.Fatal("пауза виртуальному плееру не принята автоматом")
	}
	if len(cmds) != 1 || cmds[0] != "pause" {
		t.Fatalf("источнику ушло %v", cmds)
	}
	if len(r.fwd) != 0 {
		t.Fatal("команда виртуальному плееру ушла настоящему mediad")
	}
	if r.m.Handle(nil, pauseCmd("00000000-0000-4000-8000-000000000000", "x")) {
		t.Fatal("команда чужому плееру принята как виртуальному")
	}
}

func TestHandoffPausesVirtualPlayer(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.advance(time.Second)
	r.m.Handoff()
	st := r.states()
	if last := st[len(st)-1]; !strings.HasPrefix(last, "PAUSED/") {
		t.Fatalf("при остановке шима Spotify остался играющим для платформы: %v", st)
	}
}

func TestSnapshotShowsVirtualPlayer(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	var found bool
	for _, p := range r.m.Snapshot() {
		if strings.HasPrefix(p.State, "виртуальный: играет") && p.Title == "Песня" {
			found = true
		}
	}
	if !found {
		t.Fatalf("виртуального плеера нет в снимке: %+v", r.m.Snapshot())
	}
}
