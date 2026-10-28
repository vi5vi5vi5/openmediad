package mediad

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"openmediad/internal/quasar/msg"
	"openmediad/internal/quasar/pb"
)

const timerWav = "/system/vendor/quasar/sounds/common/timer.wav"

type fakeFile struct {
	mu      sync.Mutex
	calls   []string
	pos     uint64
	elapsed uint64
}

func (f *fakeFile) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeFile) Start(id, path string, loop bool) {
	if loop {
		f.rec("start " + id + " " + path)
	} else {
		f.rec("once " + id + " " + path)
	}
}
func (f *fakeFile) Stop(id string) { f.rec("stop " + id) }
func (f *fakeFile) Pos(string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pos
}
func (f *fakeFile) Elapsed(string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.elapsed
}
func (f *fakeFile) set(pos, elapsed uint64) {
	f.mu.Lock()
	f.pos, f.elapsed = pos, elapsed
	f.mu.Unlock()
}
func (f *fakeFile) has(c string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.calls {
		if x == c {
			return true
		}
	}
	return false
}
func (f *fakeFile) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func newFileRig(t *testing.T) (*rig, *fakeFile) {
	r := newRig(t)
	ff := &fakeFile{}
	r.m.File = ff
	return r, ff
}

func filePlayer(id string) []byte {
	b := pb.AppendUint(nil, 1, 4)
	b = pb.AppendString(b, 2, id)
	return pb.AppendUint(b, 4, 1)
}

func fileStartCmd(id, path string) msg.AudioCommand {
	d := pb.AppendUint(nil, 4, 4)
	d = pb.AppendUint(d, 19, 0)
	d = pb.AppendString(d, 25, path)
	d = pb.AppendUint(d, 50, 0)
	return pb.AppendBytes(pb.AppendBytes(nil, 1, d), 22, filePlayer(id))
}

// fileOnceCmd — стартовая команда одноразового системного звука (звук после «Алиса», щелчок громкости): в дескрипторе 50:1.
func fileOnceCmd(id, path string) msg.AudioCommand {
	d := pb.AppendUint(nil, 4, 4)
	d = pb.AppendUint(d, 19, 0)
	d = pb.AppendString(d, 25, path)
	d = pb.AppendUint(d, 50, 1)
	return pb.AppendBytes(pb.AppendBytes(nil, 1, d), 22, filePlayer(id))
}

func fileReleaseCmd(id string) msg.AudioCommand {
	return pb.AppendString(pb.AppendBytes(nil, 22, filePlayer(id)), 31, "")
}

// fileRuleViolation — какие поля несёт событие плеера файлов в каждом положении. Таблица восстановлена по 908 событиям из
// записей (TestFileEventsMatchRecordings прогоняет по ней настоящие события) и применяется к тому, что выпускает автомат.
// first — это первый progress в состоянии PLAYING (до него bitrate и played_sec нет, поле 9 = 0).
func fileRuleViolation(sn PlainSnap, first bool) string {
	k, st := sn.Kind, sn.State
	switch {
	case k == 1 && st == 1: // STARTING: самое бедное событие
		if sn.Has6 || sn.Mono != 0 || sn.HasT9 || sn.HasT17 || sn.Bitrate != 0 || sn.HasPlayed {
			return "state/STARTING: лишние поля"
		}
	case k == 2 && st == 1: // progress в состоянии STARTING: длительность ещё 0, поле 9 = 0
		if !sn.Has6 || sn.Dur6 != 0 || sn.Mono == 0 || !sn.HasT9 || sn.T9 || sn.HasT17 || sn.Bitrate != 0 || sn.HasPlayed {
			return "progress/STARTING"
		}
	case k == 1 && st == 3: // PLAYING: длительность всё ещё 0
		if !sn.Has6 || sn.Dur6 != 0 || sn.Mono == 0 || sn.HasT9 || sn.HasT17 || sn.Bitrate != 0 || sn.HasPlayed {
			return "state/PLAYING"
		}
	case k == 6 && st == 3: // flag: битрейт уже есть, played_sec ещё нет
		if !sn.Has6 || sn.Dur6 == 0 || sn.Mono == 0 || sn.HasT9 || !sn.HasT17 || !sn.T17 || sn.Bitrate == 0 || sn.HasPlayed {
			return "flag/PLAYING"
		}
	case k == 2 && st == 3:
		if !sn.Has6 || sn.Dur6 == 0 || sn.Mono == 0 || !sn.HasT9 || sn.HasT17 {
			return "progress/PLAYING: базовые поля"
		}
		if first && (sn.T9 || sn.Bitrate != 0 || sn.HasPlayed) {
			return "первый progress/PLAYING: поле 9 = 0, битрейта и played_sec нет"
		}
		if !first && (sn.Bitrate == 0 || !sn.HasPlayed) {
			return "следующий progress/PLAYING: нужны bitrate и played_sec"
		}
	case k == 1 && st == 5: // ENDED
		if !sn.Has6 || sn.Dur6 == 0 || sn.Mono == 0 || sn.HasT9 || sn.HasT17 || sn.Bitrate == 0 || !sn.HasPlayed || sn.Stop == 0 {
			return "state/ENDED"
		}
	default:
		return fmt.Sprintf("неожиданное событие %v/%v", k, st)
	}
	return ""
}

// fileOnceRuleViolation — поля событий одноразового звука (50:1) по записям 28.10.2026 (TestFileOnceEventsMatchRecordings):
// у PLAYING ещё нет ни длительности, ни монотонного времени; progress один, с позицией 0; битрейт — как узнали.
func fileOnceRuleViolation(sn PlainSnap) string {
	k, st := sn.Kind, sn.State
	switch {
	case k == 1 && (st == 1 || st == 3): // STARTING и PLAYING
		if sn.Has6 || sn.Mono != 0 || sn.HasT9 || sn.HasT17 || sn.Bitrate != 0 || sn.HasPlayed || sn.Stop != 0 {
			return fmt.Sprintf("state/%v: лишние поля", st)
		}
	case k == 2 && st == 3:
		if !sn.Has6 || sn.Mono == 0 || !sn.HasT9 || sn.HasT17 || sn.Stop != 0 {
			return "progress/PLAYING: базовые поля"
		}
	case k == 6 && st == 3:
		if !sn.Has6 || sn.Mono == 0 || sn.HasT9 || !sn.HasT17 || !sn.T17 || sn.HasPlayed || sn.Stop != 0 {
			return "flag/PLAYING"
		}
	case k == 1 && st == 5:
		if !sn.Has6 || sn.Mono == 0 || sn.HasT9 || sn.HasT17 || sn.Stop == 0 {
			return "state/ENDED"
		}
	case k == 6 && st == 5: // flag уже после ENDED: у mediad под нагрузкой (8 звуков разом); автомат его не шлёт
		if !sn.Has6 || sn.Mono == 0 || sn.HasT9 || !sn.HasT17 || sn.Stop == 0 {
			return "flag/ENDED"
		}
	default:
		return fmt.Sprintf("неожиданное событие %v/%v", k, st)
	}
	return ""
}

// uuidRuleViolation — mediad даёт STARTING свой uuid (поле 3.22), а все следующие события плеера файлов несут один общий.
// evs — события одного плеера по порядку, без diag (у него дескриптора нет).
func uuidRuleViolation(evs [][]byte) string {
	var starting, common string
	for i, ev := range evs {
		e := msg.AudioEvent(ev)
		d, _ := pb.Bytes(ev, 3)
		u, _ := pb.Str(d, 22)
		switch {
		case e.Kind() == 1 && e.State() == 1:
			if i > 0 {
				return "STARTING не первым"
			}
			starting = u
		case common == "":
			if u == starting {
				return "uuid после STARTING совпал с его uuid"
			}
			common = u
		case u != common:
			return fmt.Sprintf("событие %d (%v/%v): uuid сменился", i, e.Kind(), e.State())
		}
	}
	return ""
}

func fileKinds(r *rig) []string { return kindsOf(r.events()) }

func kindsOf(evs []got) (out []string) {
	for _, g := range evs {
		out = append(out, g.kind().String()+"/"+g.state().String())
	}
	return
}

func TestFileRingSequenceTimingsAndFieldRules(t *testing.T) {
	r, ff := newFileRig(t)
	if !r.m.Handle(nil, fileStartCmd("f1", timerWav)) {
		t.Fatal("старт звонка обрабатывается машиной")
	}
	if !ff.has("start f1 " + timerWav) {
		t.Fatalf("бэкенд не запущен: %v", ff.calls)
	}
	r.advance(10 * time.Millisecond)
	if len(r.events()) != 0 {
		t.Fatal("STARTING раньше ≈25 мс приходить не должен")
	}
	r.advance(80 * time.Millisecond) // звук пошёл на 90-й миллисекунде
	r.m.FilePlaying("f1", 9273, 64)
	ff.set(0, 240)
	r.advance(300 * time.Millisecond) // flag на 240-й мс: звучало меньше секунды, played_sec ещё нет
	ff.set(1050, 1050)                // что бэкенд покажет к очередному progress
	r.advance(750 * time.Millisecond)
	ff.set(2100, 2100)
	r.advance(1050 * time.Millisecond)
	ff.set(2500, 2500) // позиция уходит вперёд, но ENDED обязан нести позицию последнего progress
	r.advance(10 * time.Millisecond)
	if !r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("release звонка обрабатывается машиной")
	}
	want := []string{"state/STARTING", "progress/STARTING", "state/PLAYING", "progress/PLAYING", "diag/PLAYING", "flag/PLAYING", "progress/PLAYING", "progress/PLAYING", "state/ENDED"}
	if got := fileKinds(r); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("события %v, ждали %v", got, want)
	}
	all := r.events()
	if !bytes.Equal(all[4].body, PlainMetaFrom(fileStartCmd("f1", timerWav)).Diag(plainSnapFrom(all[4].body))) {
		t.Fatal("diag собран не сборщиком Diag")
	}
	if p := plainSnapFrom(all[4].body); p.Bitrate != 64 {
		t.Fatalf("diag без битрейта: %+v", p)
	}
	var ev []got // без diag: у него нет ни дескриптора, ни позиции
	var bodies [][]byte
	for _, g := range all {
		if g.kind() != 5 {
			ev = append(ev, g)
			bodies = append(bodies, g.body)
		}
	}
	if v := uuidRuleViolation(bodies); v != "" {
		t.Fatal(v)
	}
	if ev[0].at != 25*time.Millisecond {
		t.Fatalf("STARTING в %v, ждали +25 мс", ev[0].at)
	}
	if ev[1].at != 90*time.Millisecond || ev[2].at != 90*time.Millisecond || ev[3].at != 90*time.Millisecond || all[4].at != 90*time.Millisecond {
		t.Fatalf("progress(STARTING), PLAYING, первый progress и diag должны идти в момент первого звука: %v %v %v %v", ev[1].at, ev[2].at, ev[3].at, all[4].at)
	}
	if d := ev[4].at - ev[2].at; d != 240*time.Millisecond {
		t.Fatalf("flag через %v после PLAYING, ждали 240 мс", d)
	}
	if d := ev[5].at - ev[3].at; d != 1050*time.Millisecond {
		t.Fatalf("progress через %v, ждали 1050 мс", d)
	}
	if d := ev[6].at - ev[5].at; d != 1050*time.Millisecond {
		t.Fatalf("progress через %v, ждали 1050 мс", d)
	}
	first := true
	for i, g := range ev {
		if g.player() != "f1" {
			t.Fatalf("чужой плеер %q", g.player())
		}
		sn := plainSnapFrom(g.body)
		isFirst := g.kind() == 2 && g.state() == 3 && first
		if g.kind() == 2 && g.state() == 3 {
			first = false
		}
		if v := fileRuleViolation(sn, isFirst); v != "" {
			t.Errorf("событие %d (%v/%v): %s", i, g.kind(), g.state(), v)
		}
		if sn.Idx != sn.PosMs/1000 {
			t.Errorf("событие %d: секунды %d при позиции %d мс", i, sn.Idx, sn.PosMs)
		}
		if sn.Dur6 != 0 && sn.Dur6 != 9 {
			t.Errorf("событие %d: длительность %d с, ждали 9 (9273 мс)", i, sn.Dur6)
		}
	}
	if p := plainSnapFrom(ev[5].body); p.PlayedSec != 1 || p.PosMs != 1050 || p.Bitrate != 64 || !p.T9 {
		t.Fatalf("второй progress: %+v", p)
	}
	last := ev[7]
	if last.pos() != ev[6].pos() || last.pos() != 2100 {
		t.Fatalf("ENDED.pos = %d, последний progress.pos = %d (ждали 2100)", last.pos(), ev[6].pos())
	}
	if p := plainSnapFrom(last.body); p.PlayedSec != 2 {
		t.Fatalf("ENDED.played_sec = %d, ждали 2 (звучало 2500 мс)", p.PlayedSec)
	}
	if !ff.has("stop f1") {
		t.Fatal("release должен остановить звук")
	}
	n := len(r.events())
	r.advance(5 * time.Second)
	if len(r.events()) != n {
		t.Fatal("после release события продолжают идти")
	}
}

func TestFileLoopRestartMakesProgressNinthFieldZeroAgain(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("f1", timerWav))
	r.advance(90 * time.Millisecond)
	r.m.FilePlaying("f1", 9273, 64)
	for i, p := range []uint64{1050, 2100, 3150, 4200, 5250, 6300, 7350, 8400, 180, 1230} {
		ff.set(p, uint64(i+1)*1050)
		r.advance(1050 * time.Millisecond)
	}
	var t9 []bool
	var pos []uint64
	for _, g := range r.events() {
		if g.kind() == 2 && g.state() == 3 {
			sn := plainSnapFrom(g.body)
			t9 = append(t9, sn.T9)
			pos = append(pos, sn.PosMs)
		}
	}
	// первый progress (поз. 0) — 0; дальше 1; progress сразу после перезапуска файла (поз. 180) — снова 0; следом опять 1
	want := []bool{false, true, true, true, true, true, true, true, true, false, true}
	if fmt.Sprint(t9) != fmt.Sprint(want) {
		t.Fatalf("поле 9 по progress: %v (позиции %v), ждали %v", t9, pos, want)
	}
}

func TestFileFastSoundStillSendsStartingFirst(t *testing.T) {
	r, _ := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("f1", timerWav))
	r.m.FilePlaying("f1", 9273, 64) // раньше, чем сработал таймер STARTING
	r.advance(100 * time.Millisecond)
	got := fileKinds(r)
	if len(got) < 4 || got[0] != "state/STARTING" || got[1] != "progress/STARTING" || got[2] != "state/PLAYING" {
		t.Fatalf("события %v: STARTING обязан идти первым и один раз", got)
	}
	n := 0
	for _, k := range got {
		if k == "state/STARTING" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("STARTING %d раз", n)
	}
}

func TestFileFailureBeforeSoundForwardsStartToMediad(t *testing.T) {
	r, ff := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	r.m.Handle("ctx", start)
	r.advance(30 * time.Millisecond) // STARTING уже ушёл
	r.m.FileFailed("f1", errors.New("нет aplay"))
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], start) {
		t.Fatalf("mediad должен получить стартовую команду, получено %d команд", len(r.fwd))
	}
	if !ff.has("stop f1") {
		t.Fatal("звук шима должен быть остановлен")
	}
	n0 := ff.count()
	if r.m.Handle(nil, start) {
		t.Fatal("повторный старт отданного звонка идёт mediad: шим не вмешивается на полпути")
	}
	if ff.count() != n0 {
		t.Fatal("отданный звонок снова запущен в шиме")
	}
	if r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("release отданного звонка идёт mediad")
	}
	if n := len(r.events()); n != 1 {
		t.Fatalf("за звонок, который ведёт mediad, шим выпускает только уже ушедший STARTING, а выпустил %d событий", n)
	}
	r.advance(10 * time.Second)
	if len(r.events()) != 1 || len(r.fwd) != 1 {
		t.Fatal("после отката шим продолжает вмешиваться")
	}
}

func TestFileStartBufferIsCopied(t *testing.T) {
	r, _ := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	orig := append([]byte(nil), start...)
	r.m.Handle(nil, start)
	for i := range start { // буфер кадра шим переиспользует
		start[i] = 0
	}
	r.m.FileFailed("f1", errors.New("сбой"))
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], orig) {
		t.Fatal("в mediad ушла не копия стартовой команды")
	}
}

func TestFileNoSoundWithinTimeoutFallsBack(t *testing.T) {
	r, _ := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	r.m.Handle(nil, start)
	r.advance(2900 * time.Millisecond)
	if len(r.fwd) != 0 {
		t.Fatal("откат раньше таймаута")
	}
	r.advance(200 * time.Millisecond)
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], start) {
		t.Fatal("после 3 с без звука стартовая команда должна уйти mediad")
	}
}

func TestFileNoTimeoutOnceSoundStarted(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("f1", timerWav))
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("f1", 9273, 64)
	for i := uint64(1); i <= 10; i++ {
		ff.set(i*1050%9273, i*1050)
		r.advance(1050 * time.Millisecond)
	}
	if len(r.fwd) != 0 {
		t.Fatal("звонок уже звучит: откат не нужен")
	}
	if k := fileKinds(r); k[len(k)-1] != "progress/PLAYING" {
		t.Fatalf("события должны идти: %v", k)
	}
}

func TestFileFailureWhileRingingEndsIt(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("f1", timerWav))
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("f1", 9273, 64)
	ff.set(1050, 1500)
	r.advance(1100 * time.Millisecond)
	r.m.FileFailed("f1", errors.New("aplay умер"))
	k := fileKinds(r)
	if k[len(k)-1] != "state/ENDED" {
		t.Fatalf("события %v: звонок оборвался, платформа должна получить ENDED", k)
	}
	if len(r.fwd) != 0 {
		t.Fatal("звук уже шёл: стартовую команду mediad отдавать нельзя, получится второй звонок")
	}
	n := len(r.events())
	r.advance(3 * time.Second)
	if len(r.events()) != n {
		t.Fatal("после ENDED события продолжают идти")
	}
	if !r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("release после ENDED обрабатывает шим")
	}
	if len(r.events()) != n {
		t.Fatal("release после ENDED не должен порождать второй ENDED")
	}
}

func TestFileReleaseWhileStillStartingEndsAndCancelsTimers(t *testing.T) {
	r, ff := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	r.m.Handle(nil, start)
	r.advance(30 * time.Millisecond)
	if !r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("release обрабатывается машиной")
	}
	if !ff.has("stop f1") {
		t.Fatal("звук должен быть остановлен")
	}
	k := fileKinds(r)
	if k[len(k)-1] != "state/ENDED" {
		t.Fatalf("события %v", k)
	}
	r.advance(10 * time.Second)
	if len(r.fwd) != 0 {
		t.Fatal("снятый звонок не должен откатываться на mediad по таймеру")
	}
	r.m.FilePlaying("f1", 9273, 64) // поздний ответ бэкенда после release игнорируется
	if n := len(r.events()); n != len(k) {
		t.Fatalf("после release появились события: %d вместо %d", n, len(k))
	}
}

func TestFileRepeatedStartDoesNotDoubleTheRing(t *testing.T) {
	r, ff := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	r.m.Handle(nil, start)
	n := ff.count()
	if !r.m.Handle(nil, start) {
		t.Fatal("повторная стартовая команда не должна уходить mediad: получится второй звонок")
	}
	if ff.count() != n {
		t.Fatal("повтор запустил звук ещё раз")
	}
}

func TestFileOutsideSystemSoundsGoesToMediad(t *testing.T) {
	r, ff := newFileRig(t)
	for _, p := range []string{"/data/x.wav", "/system/vendor/quasar/sounds/../maind", "/system/vendor/quasar/sounds/a b.wav", "/system/vendor/quasar/sounds/x;y.wav"} {
		if r.m.Handle(nil, fileStartCmd("f1", p)) {
			t.Fatalf("%q не из системных звуков: команда должна идти mediad", p)
		}
	}
	if ff.count() != 0 {
		t.Fatal("бэкенд не должен трогать чужие файлы")
	}
	if r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("release чужого плеера идёт mediad")
	}
	r.m.FileOK = func(p string) bool { return p == "/data/x.wav" }
	if !r.m.Handle(nil, fileStartCmd("f2", "/data/x.wav")) {
		t.Fatal("FileOK разрешил файл: машина обязана его взять")
	}
}

const spotterStart = "/system/vendor/quasar/sounds/common/spotter_start.mp3"

// oneShotEvents — события без diag и сами diag отдельно.
func oneShotEvents(r *rig) (ev []got, diag []got) {
	for _, g := range r.events() {
		if g.kind() == 5 {
			diag = append(diag, g)
		} else {
			ev = append(ev, g)
		}
	}
	return
}

// Звук после «Алиса» играет один раз, и конец сообщает сам плеер: release платформа присылает только после ENDED. Порядок
// и поля событий — как у mediad в записях 28.10.2026 (BUS-PROTOCOL.md, §2.6).
func TestFileOneShotPlaysOnceAndEndsByItself(t *testing.T) {
	r, ff := newFileRig(t)
	start := fileOnceCmd("o1", spotterStart)
	if !r.m.Handle(nil, start) {
		t.Fatal("одноразовый звук теперь играет шим")
	}
	if !ff.has("once o1 " + spotterStart) {
		t.Fatalf("бэкенд должен играть файл один раз: %v", ff.calls)
	}
	r.advance(30 * time.Millisecond)
	r.m.FilePlaying("o1", 576, 80) // 0,58 с, средний битрейт 80
	r.advance(300 * time.Millisecond)
	ff.set(576, 576)
	r.m.FileEnded("o1")
	ev, diag := oneShotEvents(r)
	want := []string{"state/STARTING", "state/PLAYING", "progress/PLAYING", "flag/PLAYING", "state/ENDED"}
	if got := kindsOf(ev); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("события %v, ждали %v", got, want)
	}
	if len(diag) != 1 || diag[0].at != ev[2].at {
		t.Fatalf("diag с битрейтом должен идти один раз, сразу за первым progress: %d", len(diag))
	}
	if p := plainSnapFrom(diag[0].body); p.Bitrate != 80 || !bytes.Equal(diag[0].body, PlainMetaFrom(start).Diag(p)) {
		t.Fatalf("diag: %+v", p)
	}
	if d := ev[3].at - ev[1].at; d != 210*time.Millisecond {
		t.Fatalf("flag через %v после PLAYING, ждали 210 мс", d)
	}
	var bodies [][]byte
	for i, g := range ev {
		bodies = append(bodies, g.body)
		sn := plainSnapFrom(g.body)
		if v := fileOnceRuleViolation(sn); v != "" {
			t.Errorf("событие %d: %s", i, v)
		}
		if sn.PosMs != 0 || (sn.Has6 && sn.Dur6 != 0) {
			t.Errorf("событие %d: позиция %d мс, длительность %d с; ждали 0 и 0 (0,58 с — целых секунд нет)", i, sn.PosMs, sn.Dur6)
		}
		if g.kind() != 1 || g.state() != 1 {
			if (g.kind() == 6 || g.state() == 5) && sn.Bitrate != 80 {
				t.Errorf("событие %d (%v/%v): нет битрейта", i, g.kind(), g.state())
			}
		}
	}
	if v := uuidRuleViolation(bodies); v != "" {
		t.Fatal(v)
	}
	if len(r.fwd) != 0 {
		t.Fatal("mediad ничего не получает")
	}
	n := len(r.events())
	if !r.m.Handle(nil, fileReleaseCmd("o1")) {
		t.Fatal("release после ENDED — наш: он не должен уйти mediad")
	}
	if len(r.events()) != n {
		t.Fatal("release после ENDED не порождает событий")
	}
	if s := r.m.Snapshot(); len(s) != 0 {
		t.Fatalf("после release плеер забыт: %+v", s)
	}
}

// Щелчок громкости (0,1 с) кончается раньше, чем пришло бы время flag; битрейта у файла без заголовка Xing нет — нет и diag.
func TestFileOneShotShortSoundHasNoFlagAndNoDiag(t *testing.T) {
	r, _ := newFileRig(t)
	r.m.Handle(nil, fileOnceCmd("v", "/system/vendor/quasar/sounds/common/volume_sound.mp3"))
	r.advance(30 * time.Millisecond)
	r.m.FilePlaying("v", 104, 0)
	r.advance(130 * time.Millisecond)
	r.m.FileEnded("v")
	r.advance(2 * time.Second)
	want := []string{"state/STARTING", "state/PLAYING", "progress/PLAYING", "state/ENDED"}
	if got := fileKinds(r); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("события %v, ждали %v", got, want)
	}
	for _, g := range r.events() {
		if v := fileOnceRuleViolation(plainSnapFrom(g.body)); v != "" {
			t.Errorf("%v/%v: %s", g.kind(), g.state(), v)
		}
	}
}

// Release раньше конца звука (платформа передумала) обрывает звук и сразу даёт ENDED, как у звонка.
func TestFileOneShotReleaseBeforeEndStopsIt(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileOnceCmd("o", spotterStart))
	r.advance(30 * time.Millisecond)
	r.m.FilePlaying("o", 576, 80)
	r.advance(100 * time.Millisecond)
	if !r.m.Handle(nil, fileReleaseCmd("o")) || !ff.has("stop o") {
		t.Fatal("release обрывает звук")
	}
	k := fileKinds(r)
	if k[len(k)-1] != "state/ENDED" {
		t.Fatalf("события %v", k)
	}
	n := len(r.events())
	r.m.FileEnded("o") // поздний конец звука после release
	r.advance(time.Second)
	if len(r.events()) != n {
		t.Fatal("второго ENDED быть не должно")
	}
}

// Не зазвучал за 3 с — звук играет mediad (откат), как у звонка; release тоже идёт ему.
func TestFileOneShotFailureForwardsToMediad(t *testing.T) {
	r, _ := newFileRig(t)
	start := fileOnceCmd("o", spotterStart)
	r.m.Handle("ctx", start)
	r.advance(3100 * time.Millisecond)
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], start) {
		t.Fatal("стартовая команда должна уйти mediad")
	}
	if r.m.Handle(nil, fileReleaseCmd("o")) {
		t.Fatal("release отданного звука идёт mediad")
	}
}

// ENDED ушёл, а release так и не пришёл: плеер не висит вечно.
func TestFileOneShotForgottenWithoutRelease(t *testing.T) {
	r, _ := newFileRig(t)
	r.m.Handle(nil, fileOnceCmd("o", spotterStart))
	r.advance(30 * time.Millisecond)
	r.m.FilePlaying("o", 576, 80)
	r.advance(600 * time.Millisecond)
	r.m.FileEnded("o")
	if s := r.m.Snapshot(); len(s) != 1 || s[0].State != "системный звук: доиграл" {
		t.Fatalf("до release плеер ждёт его: %+v", s)
	}
	r.advance(31 * time.Second)
	if s := r.m.Snapshot(); len(s) != 0 {
		t.Fatalf("без release плеер забыт через 30 с: %+v", s)
	}
}

// Остановка шима: звучащий одноразовый звук не повторяется у mediad (он короткий и уже звучал), платформа получает ENDED;
// звонок рядом по-прежнему передаётся mediad.
func TestHandoffEndsPlayingOneShotAndForwardsRing(t *testing.T) {
	r, ff := newFileRig(t)
	ring := fileStartCmd("ring", timerWav)
	r.m.Handle("ctx", ring)
	r.m.Handle("ctx", fileOnceCmd("o", spotterStart))
	r.advance(30 * time.Millisecond)
	r.m.FilePlaying("ring", 9273, 64)
	r.m.FilePlaying("o", 576, 80)
	r.advance(100 * time.Millisecond)
	before := len(r.events())
	r.m.Handoff()
	if !ff.has("stop o") || !ff.has("stop ring") {
		t.Fatalf("оба звука остановлены: %v", ff.calls)
	}
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], ring) {
		t.Fatal("mediad получает только звонок")
	}
	evs := r.events()[before:]
	if len(evs) != 1 || evs[0].player() != "o" || evs[0].state() != 5 {
		t.Fatalf("после передачи — только ENDED одноразового звука: %d событий", len(evs))
	}
}

func TestFileIgnoredWithoutBackend(t *testing.T) {
	r := newRig(t)
	if r.m.Handle(nil, fileStartCmd("f1", timerWav)) || r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("без бэкенда звонки идут mediad")
	}
}

func TestHandoffForwardsActiveRingToMediad(t *testing.T) {
	r, ff := newFileRig(t)
	start := fileStartCmd("f1", timerWav)
	r.m.Handle("ctx", start)
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("f1", 9273, 64)
	r.advance(500 * time.Millisecond)
	n := len(r.events())
	r.m.Handoff()
	if !ff.has("stop f1") {
		t.Fatal("звук шима должен быть остановлен")
	}
	if len(r.fwd) != 1 || !bytes.Equal(r.fwd[0], start) {
		t.Fatal("звонок должен продолжиться у mediad: ему отдаётся стартовая команда")
	}
	if len(r.events()) != n {
		t.Fatal("ENDED при передаче не нужен: платформа считает звонок идущим")
	}
	r.advance(5 * time.Second)
	if len(r.events()) != n {
		t.Fatal("после передачи события продолжают идти")
	}
	if r.m.Handle(nil, fileReleaseCmd("f1")) {
		t.Fatal("после передачи release этого звонка идёт mediad")
	}
}

// Звонок оборвался посреди звучания (платформа получила ENDED), release ещё не пришёл, и тут демон останавливается: mediad
// не должен получить стартовую команду — он зазвонил бы заново.
func TestHandoffSkipsEndedRing(t *testing.T) {
	r, _ := newFileRig(t)
	r.m.Handle("ctx", fileStartCmd("f1", timerWav))
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("f1", 9273, 64)
	r.advance(500 * time.Millisecond)
	r.m.FileFailed("f1", errors.New("aplay умер"))
	n := len(r.events())
	r.m.Handoff()
	if len(r.fwd) != 0 {
		t.Fatal("законченный звонок не передаётся mediad")
	}
	if len(r.events()) != n {
		t.Fatal("второго ENDED быть не должно")
	}
}

func TestSnapshotShowsRing(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("ring-1234567890", timerWav))
	if s := r.m.Snapshot(); len(s) != 1 || s[0].State != "звонок: запускается" || s[0].Title != timerWav {
		t.Fatalf("снимок до звука: %+v", s)
	}
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("ring-1234567890", 9273, 64)
	ff.set(777, 777)
	if s := r.m.Snapshot(); len(s) != 1 || s[0].State != "звонок: звучит" || s[0].PosMs != 777 || s[0].DurMs != 9273 || s[0].ID != "ring-123" {
		t.Fatalf("снимок со звуком: %+v", s)
	}
}

func TestFileNonMP3HasNoBitrate(t *testing.T) {
	r, ff := newFileRig(t)
	r.m.Handle(nil, fileStartCmd("f1", "/system/vendor/quasar/sounds/common/bluetooth_turning_on_jingle.opus"))
	r.advance(50 * time.Millisecond)
	r.m.FilePlaying("f1", 1500, 0)
	ff.set(1050, 1050)
	r.advance(1100 * time.Millisecond)
	for _, g := range r.events() {
		if sn := plainSnapFrom(g.body); sn.Bitrate != 0 {
			t.Fatalf("у файла без известного битрейта diag не должен нести bitrate (%v/%v)", g.kind(), g.state())
		}
	}
}
