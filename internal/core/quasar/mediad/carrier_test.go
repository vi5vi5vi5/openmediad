package mediad

import (
	"testing"
	"time"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
)

// Занятый плеер платформы: Spotify подставляет своё состояние под player id, который платформа считает текущим.

const (
	yandexPlayer = "0b0b0b0b-1111-4222-8333-444455556666"
	yandexTrack  = "121489368"
	otherPlayer  = "11111111-2222-4333-8444-555566667777"
)

// eventTitle — название трека из описания события (3.9.1).
func eventTitle(g got) string {
	d, _ := pb.Bytes(g.body, 3)
	md, _ := pb.Bytes(d, 9)
	t, _ := pb.Str(md, 1)
	return t
}

func playersOf(evs []got) map[string]bool {
	out := map[string]bool{}
	for _, g := range evs {
		out[g.player()] = true
	}
	return out
}

func TestArmedCarrierTurnsYandexPlayIntoSpotify(t *testing.T) {
	r := newRig(t)
	var cmds []string
	r.m.OnVirtualCommand(func(c string) { cmds = append(cmds, c) })
	r.m.SetVirtual(vtrack(true))
	own := r.events()[0].player()
	n := len(r.events())
	r.m.ArmCarrier(3 * time.Second)
	if !r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0)) {
		t.Fatal("запуск Яндекса не принят автоматом")
	}
	if len(r.fwd) != 0 || r.be.has("open "+yandexPlayer+" "+yandexTrack) {
		t.Fatalf("запуск Яндекса дошёл до звука или до mediad: fwd=%d calls=%v", len(r.fwd), r.be.calls)
	}
	evs := r.events()[n:]
	var st []string
	for _, g := range evs {
		if g.kind() == 1 {
			st = append(st, g.state().String()+"/"+g.player())
		}
	}
	if len(st) != 2 || st[0] != "STARTING/"+yandexPlayer || st[1] != "PLAYING/"+yandexPlayer {
		t.Fatalf("платформа должна увидеть запуск своего плеера: %v", st)
	}
	if got := eventTitle(evs[0]); got != "Песня" {
		t.Fatalf("название в событии плеера платформы %q, ждали название Spotify", got)
	}
	if playersOf(evs)[own] {
		t.Fatal("после занятия плеера события идут под прежним собственным id")
	}
	// прогресс идёт под id платформы, команда платформы доходит до Spotify
	r.advance(2 * time.Second)
	for _, g := range r.events()[n:] {
		if g.player() != yandexPlayer {
			t.Fatalf("событие под чужим id %s", g.player())
		}
	}
	if !r.m.Handle(nil, pauseCmd(yandexPlayer, yandexTrack)) || len(cmds) != 1 || cmds[0] != "pause" {
		t.Fatalf("пауза плееру платформы: команды Spotify %v", cmds)
	}
	// окно закрылось: следующий запуск Яндекса играется как обычно
	r.m.Handle(nil, playCmd(otherPlayer, "5", 0))
	if !r.be.has("open " + otherPlayer + " 5") {
		t.Fatal("после занятия плеера следующий запуск тоже проглочен")
	}
}

func TestCarrierNotArmedOrExpiredPlaysNormally(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	if !r.be.has("open " + yandexPlayer + " " + yandexTrack) {
		t.Fatal("без ArmCarrier запуск Яндекса должен играться")
	}
	r2 := newRig(t)
	r2.m.SetVirtual(vtrack(true))
	r2.m.ArmCarrier(3 * time.Second)
	r2.advance(5 * time.Second)
	r2.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	if !r2.be.has("open " + yandexPlayer + " " + yandexTrack) {
		t.Fatal("по истечении окна запуск Яндекса должен играться")
	}
}

func TestArmedPrefetchIsAcceptedQuietly(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	n := len(r.events())
	r.m.ArmCarrier(3 * time.Second)
	if !r.m.Handle(nil, prefetchCmd(yandexPlayer, yandexTrack)) {
		t.Fatal("prefetch не принят")
	}
	if len(r.events()) != n || r.be.has("open "+yandexPlayer+" "+yandexTrack) || len(r.fwd) != 0 {
		t.Fatal("prefetch должен приниматься молча")
	}
	if !r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0)) || len(r.events()) == n {
		t.Fatal("после prefetch окно должно остаться для play")
	}
}

func TestAdoptedSessionKeepsPlayerAcrossTracksAndStops(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.m.ArmCarrier(3 * time.Second)
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	n := len(r.events())
	next := vtrack(true)
	next.Track, next.Title, next.PosMs = "spotify:track:2", "Другая песня", 0
	r.m.SetVirtual(next)
	evs := r.events()[n:]
	for _, g := range evs {
		if g.player() != yandexPlayer || g.kind() == 1 && g.state() == msg.PlayState(5) {
			t.Fatalf("смена трека в сессии платформы: плеер %s состояние %v", g.player(), g.state())
		}
	}
	if len(evs) == 0 || eventTitle(evs[0]) != "Другая песня" {
		t.Fatal("платформа не узнала название нового трека")
	}
	// остановка Spotify: ENDED запустил бы у облака очередь Яндекса, поэтому только пауза
	n = len(r.events())
	r.m.SetVirtual(nil)
	evs = r.events()[n:]
	if len(evs) != 1 || evs[0].state() != msg.PlayState(4) || evs[0].player() != yandexPlayer {
		t.Fatalf("остановка Spotify в сессии платформы: %v", r.states())
	}
	// и играет снова в той же сессии
	n = len(r.events())
	r.m.SetVirtual(next)
	if evs = r.events()[n:]; len(evs) == 0 || evs[0].player() != yandexPlayer || evs[0].state() != msg.PlayState(3) {
		t.Fatalf("возобновление в сессии платформы: %v", r.states())
	}
}

func TestTakeOverGivesYandexPlayerToSpotify(t *testing.T) {
	r := newRig(t)
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	r.advance(2 * time.Second)
	r.m.SetVirtual(vtrack(true)) // Spotify заиграл: сначала своё описание
	n := len(r.events())
	if got := r.m.TakeOver(); got != 1 {
		t.Fatalf("TakeOver остановил %d плееров, ждали 1", got)
	}
	evs := r.events()[n:]
	var sawPlaying bool
	for _, g := range evs {
		if g.state() == msg.PlayState(4) {
			t.Fatalf("плеер Яндекса получил PAUSED: платформа решила бы, что музыка встала")
		}
		if g.player() != yandexPlayer {
			t.Fatalf("после TakeOver событие под чужим id %s", g.player())
		}
		if g.kind() == 1 && g.state() == msg.PlayState(3) && eventTitle(g) == "Песня" {
			sawPlaying = true
		}
	}
	if !sawPlaying {
		t.Fatalf("платформа не увидела Spotify под плеером Яндекса: %v", r.states())
	}
	for end := time.Now().Add(time.Second); !r.be.has("close "+yandexPlayer) && time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
	}
	if !r.be.has("close " + yandexPlayer) {
		t.Fatalf("звук Яндекса не остановлен: %v", r.be.calls)
	}
	if r.m.Playing() {
		t.Fatal("после TakeOver автомат всё ещё считает, что Яндекс играет")
	}
}

func TestTakeOverBeforeSpotifyStateWaitsForIt(t *testing.T) {
	r := newRig(t)
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	r.advance(2 * time.Second)
	if r.m.TakeOver() != 1 {
		t.Fatal("TakeOver не нашёл плеер")
	}
	n := len(r.events())
	r.m.SetVirtual(vtrack(true))
	for _, g := range r.events()[n:] {
		if g.player() != yandexPlayer {
			t.Fatalf("первое состояние Spotify ушло под собственным id %s", g.player())
		}
	}
	if len(r.events()) == n {
		t.Fatal("состояние Spotify не дошло до платформы")
	}
}

func TestTakeOverWithoutYandexDoesNothing(t *testing.T) {
	r := newRig(t)
	if r.m.TakeOver() != 0 || len(r.events()) != 0 {
		t.Fatal("TakeOver без играющего Яндекса что-то сделал")
	}
}

func TestYandexMusicStartedByPlatformDropsAdoptedSession(t *testing.T) {
	r := newRig(t)
	r.m.SetVirtual(vtrack(true))
	r.m.ArmCarrier(3 * time.Second)
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0))
	if !r.m.isVirtual(yandexPlayer) {
		t.Fatal("плеер не занят")
	}
	r.advance(5 * time.Second) // окно закрыто
	r.m.Handle(nil, playCmd(otherPlayer, "7", 0))
	if r.m.isVirtual(yandexPlayer) {
		t.Fatal("после запуска настоящей музыки занятый плеер остался")
	}
}

func TestRetitleKeepsOtherFields(t *testing.T) {
	md := pb.AppendString(nil, 1, "Старое")
	md = pb.AppendString(md, 2, "Старый")
	md = pb.AppendString(md, 5, "лишнее")
	desc := pb.AppendString(nil, 1, "id")
	desc = pb.AppendBytes(desc, 9, md)
	desc = pb.AppendString(desc, 39, "ключ")
	out := retitle(desc, "Новое", "Новый")
	m, _ := pb.Bytes(out, 9)
	if s, _ := pb.Str(m, 1); s != "Новое" {
		t.Fatalf("название %q", s)
	}
	if s, _ := pb.Str(m, 2); s != "Новый" {
		t.Fatalf("исполнитель %q", s)
	}
	if s, _ := pb.Str(m, 5); s != "лишнее" {
		t.Fatalf("чужое поле метаданных потеряно: %q", s)
	}
	if s, _ := pb.Str(out, 39); s != "ключ" {
		t.Fatalf("поле дескриптора потеряно: %q", s)
	}
	bare := retitle(pb.AppendString(nil, 1, "id"), "А", "Б")
	m, _ = pb.Bytes(bare, 9)
	if s, _ := pb.Str(m, 1); s != "А" {
		t.Fatalf("метаданные не добавлены: %q", s)
	}
}

// Платформа заменяет запуск: перед новым audio_play она «ставит на паузу» текущий плеер. Источник уже выполнил фразу, и эта пауза
// остановила бы его (так «дальше» оставляло трек на паузе, а повторное «играй» не работало).
func TestPlatformPauseWhileReplacingTheLaunchIsNotPassedOn(t *testing.T) {
	r := newRig(t)
	var cmds []string
	r.m.OnVirtualCommand(func(c string) { cmds = append(cmds, c) })
	r.m.SetVirtual(vtrack(true))
	r.m.ArmCarrier(3 * time.Second)
	r.m.Handle(nil, playCmd(yandexPlayer, yandexTrack, 0)) // сессия платформы занята
	r.m.ArmCarrier(3 * time.Second)                        // следующая быстрая команда
	if !r.m.Handle(nil, pauseCmd(yandexPlayer, yandexTrack)) {
		t.Fatal("пауза плееру платформы не принята автоматом")
	}
	if len(cmds) != 0 {
		t.Fatalf("пауза при замене запуска дошла до источника: %v", cmds)
	}
	if len(r.fwd) != 0 {
		t.Fatal("команда виртуальному плееру ушла настоящему mediad")
	}
	// запуск, которого ждали, принимается как раньше
	if !r.m.Handle(nil, playCmd(otherPlayer, "5", 0)) || !r.m.isVirtual(otherPlayer) {
		t.Fatal("запуск после проглоченной паузы не занял плеер")
	}
	// окно закрыто: пауза (кнопка) доходит до источника
	r.m.Handle(nil, pauseCmd(otherPlayer, "5"))
	if len(cmds) != 1 || cmds[0] != "pause" {
		t.Fatalf("пауза вне окна не дошла до источника: %v", cmds)
	}
}
