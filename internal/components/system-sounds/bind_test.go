package systemsounds

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
)

// fakeSink — приёмник в памяти: копит записанное и «играет» в десять раз быстрее реального времени, чтобы запись не вращалась вхолостую.
type fakeSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	aborted atomic.Bool
	failAt  int // после стольких записанных байт Write возвращает ошибку (0 — никогда)
}

func (f *fakeSink) Write(p []byte) (int, error) {
	if f.aborted.Load() {
		return 0, errors.New("вывод закрыт")
	}
	f.mu.Lock()
	f.buf.Write(p)
	n := f.buf.Len()
	f.mu.Unlock()
	if f.failAt > 0 && n >= f.failAt {
		return 0, errors.New("aplay умер")
	}
	time.Sleep(time.Duration(len(p)/bytesPerMs) * time.Millisecond / 10)
	return len(p), nil
}

func (f *fakeSink) Abort() { f.aborted.Store(true) }
func (f *fakeSink) Drain() {}

const bytesPerMs = 192 // стерео S16LE 48 кГц

func (f *fakeSink) bytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.buf.Bytes()...)
}

// pattern — PCM из n байт с повторяющимся, но не кратным размеру записи узором: сбой на стыке проходов виден по смещению.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

type rig struct {
	b        *Backend
	newSink  func() (*fakeSink, error) // подмена вывода (nil — обычный fakeSink)
	sinks    []*fakeSink
	decodes  atomic.Int32
	playing  chan [3]any
	ended    chan string
	failed   chan error
	mu       sync.Mutex
	newSinks int
}

func newRig(clip *Clip, decErr error) *rig {
	r := &rig{playing: make(chan [3]any, 8), ended: make(chan string, 8), failed: make(chan error, 8)}
	eng := &engine.Engine{Router: engine.RouterFunc(func(engine.Class, audio.Format) (audio.Output, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.newSinks++
		mk := r.newSink
		if mk == nil {
			mk = func() (*fakeSink, error) { return &fakeSink{}, nil }
		}
		s, err := mk()
		if err != nil {
			return nil, err
		}
		r.sinks = append(r.sinks, s)
		return s, nil
	})}
	r.b = &Backend{Engine: eng, Events: r, Lib: &Library{Decode: func(path string) (*Clip, error) {
		r.decodes.Add(1)
		if decErr != nil {
			return nil, decErr
		}
		return clip, nil
	}}}
	return r
}

// события Backend для автомата mediad
func (r *rig) FilePlaying(id string, durMs, bitrate uint64) { r.playing <- [3]any{id, durMs, bitrate} }
func (r *rig) FileEnded(id string)                          { r.ended <- id }
func (r *rig) FileFailed(id string, err error)              { r.failed <- err }

func (r *rig) sink(i int) *fakeSink {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sinks[i]
}

func TestLoopsGaplesslyUntilStopped(t *testing.T) {
	pcm := pattern(20000) // три записи на проход: 8192 + 8192 + 3616
	r := newRig(&Clip{PCM: pcm, Bitrate: 64}, nil)
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	select {
	case p := <-r.playing:
		if p[0] != "a" || p[1] != uint64(20000/bytesPerMs) || p[2] != uint64(64) {
			t.Fatalf("OnPlaying = %v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnPlaying")
	}
	sk := r.sink(0)
	waitFor(t, "три прохода", func() bool { return len(sk.bytes()) > 3*len(pcm) })
	r.b.Stop("a")
	waitFor(t, "aplay оборван", sk.aborted.Load)
	got := sk.bytes()
	for i, b := range got {
		if b != pcm[i%len(pcm)] {
			t.Fatalf("байт %d = %d, ждали %d: проход начался не с начала файла", i, b, pcm[i%len(pcm)])
		}
	}
	n := len(got)
	time.Sleep(30 * time.Millisecond)
	if len(sk.bytes()) != n {
		t.Fatal("после Stop запись продолжается")
	}
	if len(r.playing) != 0 {
		t.Fatal("OnPlaying вызван повторно")
	}
	if p, el := r.b.Pos("a"), r.b.Elapsed("a"); p != 0 || el != 0 {
		t.Fatalf("после Stop позиция %d, прошло %d", p, el)
	}
}

func TestDecodeFailureReportsAndNeverPlays(t *testing.T) {
	r := newRig(nil, errors.New("gst упал"))
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	select {
	case err := <-r.failed:
		if !strings.Contains(err.Error(), "gst упал") || !strings.Contains(err.Error(), "раскодирование") {
			t.Fatalf("ошибка = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnFailed")
	}
	// ошибка приходит, когда вывод мог ещё не открыться: тогда aplay не запускается вовсе, а открытый — обрывается
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	sinks := append([]*fakeSink(nil), r.sinks...)
	r.mu.Unlock()
	for i, sk := range sinks {
		if !sk.aborted.Load() {
			t.Fatalf("aplay %d после сбоя не остановлен", i)
		}
	}
	if len(r.playing) != 0 {
		t.Fatal("OnPlaying после сбоя")
	}
	if r.b.get("a") != nil {
		t.Fatal("сбойный звонок остался в списке")
	}
}

func TestEmptyClipIsAFailure(t *testing.T) {
	r := newRig(&Clip{}, nil)
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	select {
	case err := <-r.failed:
		if !strings.Contains(err.Error(), "нет звука") {
			t.Fatalf("ошибка = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnFailed")
	}
}

func TestSinkStartFailureReports(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(4000)}, nil)
	r.newSink = func() (*fakeSink, error) { return nil, errors.New("нет aplay") }
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	select {
	case err := <-r.failed:
		if !strings.Contains(err.Error(), "запуск вывода") || !strings.Contains(err.Error(), "нет aplay") {
			t.Fatalf("ошибка = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnFailed")
	}
}

func TestSinkDyingMidRingReports(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(20000)}, nil)
	r.newSink = func() (*fakeSink, error) { return &fakeSink{failAt: 30000}, nil }
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	select {
	case err := <-r.failed:
		if !strings.Contains(err.Error(), "оборвался") { // звук уже шёл: автомат закончит звонок событием ENDED
			t.Fatalf("ошибка = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnFailed")
	}
	if len(r.playing) != 1 {
		t.Fatal("звук шёл, OnPlaying должен был сработать один раз")
	}
	waitFor(t, "aplay остановлен", r.sink(0).aborted.Load)
}

func TestStopWhileStillDecodingStaysSilent(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(4000)}, nil)
	gate := make(chan struct{})
	r.b.Lib.Decode = func(string) (*Clip, error) {
		<-gate
		return &Clip{PCM: pattern(4000)}, nil
	}
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	waitFor(t, "aplay поднят параллельно с раскодированием", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.newSinks == 1
	})
	r.b.Stop("a")
	close(gate)
	waitFor(t, "aplay остановлен", r.sink(0).aborted.Load)
	time.Sleep(20 * time.Millisecond)
	if len(r.playing) != 0 || len(r.failed) != 0 {
		t.Fatal("остановленный звонок не должен ни играть, ни жаловаться")
	}
	if len(r.sink(0).bytes()) != 0 {
		t.Fatal("в остановленный aplay что-то записано")
	}
}

func TestSecondStartOfSameFileUsesTheCache(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(4000)}, nil)
	path := "/system/vendor/quasar/sounds/common/timer.wav"
	if err := r.b.Preload(path); err != nil {
		t.Fatal(err)
	}
	r.b.Start("a", path, true)
	<-r.playing
	r.b.Stop("a")
	r.b.Start("b", path, true)
	<-r.playing
	r.b.Stop("b")
	if n := r.decodes.Load(); n != 1 {
		t.Fatalf("файл раскодирован %d раз, ждали 1", n)
	}
}

func TestCacheEvictsOldestAndSkipsHugeClips(t *testing.T) {
	l := &Library{}
	mb := func(n int) *Clip { return &Clip{PCM: make([]byte, n<<20)} }
	l.remember("a", mb(3))
	l.remember("b", mb(2))
	l.remember("c", mb(2)) // 3+2+2 > 6: уходит самый старый
	if l.cache["a"] != nil || l.cache["b"] == nil || l.cache["c"] == nil || l.bytes != 4<<20 {
		t.Fatalf("кэш после вытеснения: a=%v b=%v c=%v байт=%d", l.cache["a"] != nil, l.cache["b"] != nil, l.cache["c"] != nil, l.bytes)
	}
	l.remember("huge", mb(7))
	if l.cache["huge"] != nil {
		t.Fatal("звук больше лимита попал в кэш")
	}
}

func TestShutdownStopsEveryRingAndRefusesNewOnes(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(20000)}, nil)
	r.b.Start("a", "/system/vendor/quasar/sounds/common/timer.wav", true)
	r.b.Start("b", "/system/vendor/quasar/sounds/common/timer.wav", true)
	<-r.playing
	<-r.playing
	r.b.Shutdown()
	if !r.sink(0).aborted.Load() || !r.sink(1).aborted.Load() {
		t.Fatal("после Shutdown (он синхронный) не все aplay остановлены")
	}
	r.b.Start("c", "/system/vendor/quasar/sounds/common/timer.wav", true)
	time.Sleep(20 * time.Millisecond)
	if r.decodes.Load() != 1 || len(r.sinks) != 2 {
		t.Fatal("после Shutdown запущен новый звонок")
	}
}

func TestSameIDRestartReplacesTheOldRing(t *testing.T) {
	r := newRig(&Clip{PCM: pattern(20000)}, nil)
	path := "/system/vendor/quasar/sounds/common/timer.wav"
	r.b.Start("a", path, true)
	<-r.playing
	r.b.Start("a", path, true)
	<-r.playing
	waitFor(t, "старый aplay остановлен", r.sink(0).aborted.Load)
	if r.sink(1).aborted.Load() {
		t.Fatal("новый звонок остановлен вместе со старым")
	}
	r.b.Stop("a")
}

func TestValidPath(t *testing.T) {
	ok := []string{
		"/system/vendor/quasar/sounds/common/timer.wav",
		"/system/vendor/quasar/sounds/ru-RU/alarm_1.mp3",
	}
	bad := []string{
		"",
		"/system/vendor/quasar/sounds/",
		"/system/vendor/quasar/sounds/../maind",
		"/system/vendor/quasar/sounds/common/a b.wav",
		"/system/vendor/quasar/sounds/common/a;b.wav",
		"/system/vendor/quasar/sounds/common/a!b.wav",
		"/system/vendor/quasar/sounds/common/$(x).wav",
		"/system/vendor/quasar/soundsX/timer.wav",
		"/tmp/timer.wav",
		"/system/vendor/quasar/sounds/common/\"x\".wav",
		"/system/vendor/quasar/sounds/common/тихо.wav",
	}
	for _, p := range ok {
		if !ValidPath(p) {
			t.Errorf("ValidPath(%q) = false", p)
		}
	}
	for _, p := range bad {
		if ValidPath(p) {
			t.Errorf("ValidPath(%q) = true", p)
		}
	}
}

// Одноразовый системный звук играет файл один раз: весь звук ровно один раз, вывод доигрывается (не обрывается), конец —
// FileEnded (не FileFailed). Позиция после конца — длительность файла, не ноль (у звонка она шла бы по кругу).
func TestOnceSoundPlaysOnceAndReportsEnd(t *testing.T) {
	pcm := pattern(20000)
	r := newRig(&Clip{PCM: pcm, Bitrate: 80}, nil)
	r.b.Start("o", "/system/vendor/quasar/sounds/common/spotter_start.mp3", false)
	select {
	case p := <-r.playing:
		if p[0] != "o" || p[2] != uint64(80) {
			t.Fatalf("OnPlaying = %v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("нет OnPlaying")
	}
	select {
	case id := <-r.ended:
		if id != "o" {
			t.Fatalf("FileEnded(%q)", id)
		}
	case err := <-r.failed:
		t.Fatalf("конец одноразового звука — не ошибка: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("нет FileEnded")
	}
	sk := r.sink(0)
	if got := sk.bytes(); !bytes.Equal(got, pcm) {
		t.Fatalf("сыграно %d байт, ждали ровно один проход (%d)", len(got), len(pcm))
	}
	if sk.aborted.Load() {
		t.Fatal("доигранный звук не обрывают: aplay доигрывает остаток")
	}
	dur := uint64(20000 / bytesPerMs)
	waitFor(t, "позиция после конца = длительность", func() bool { p := r.b.Pos("o"); return p >= dur-1 && p <= dur })
	r.b.Stop("o") // release платформы после ENDED
	if r.b.get("o") != nil {
		t.Fatal("после Stop звук должен быть забыт")
	}
}
