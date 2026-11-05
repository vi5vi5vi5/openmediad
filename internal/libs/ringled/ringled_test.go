package ringled

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"openmediad/internal/core/led"
)

type fakeDev struct {
	mu     sync.Mutex
	cur    Frame
	writes []Frame
	reads  int
	fail   bool

	dropZero bool // погасить кольцо не удаётся: запись нуля теряется
}

func (d *fakeDev) Read() (Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	return d.cur, nil
}

func (d *fakeDev) Write(f Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return os.ErrPermission
	}
	d.writes = append(d.writes, f)
	if d.dropZero && f.IsZero() {
		return nil // запись «дошла», а кольцо не изменилось
	}
	d.cur = f
	return nil
}

// firmware — прошивка нарисовала что-то своё.
func (d *fakeDev) firmware(f Frame) { d.mu.Lock(); d.cur = f; d.mu.Unlock() }
func (d *fakeDev) nwrites() int     { d.mu.Lock(); defer d.mu.Unlock(); return len(d.writes) }

type rig struct {
	dev *fakeDev
	k   *Keeper
	now time.Time
	an  Anim
}

func newRig() *rig {
	r := &rig{dev: &fakeDev{}, now: time.Unix(1000, 0)}
	r.k = &Keeper{Dev: r.dev, Anim: func() Anim { return r.an }, Now: func() time.Time { return r.now }}
	return r
}

func (r *rig) advance(d time.Duration) { r.now = r.now.Add(d); r.k.Tick(r.now) }

var (
	yellow = led.Color{R: 255, G: 176, B: 0}
	blue   = led.Color{R: 0, G: 96, B: 255}
)

func frame(zone int, c led.Color) (f Frame) { f[zone] = c; return }

func TestSetDrawsImmediatelyWhenFirmwareIsIdle(t *testing.T) {
	r := newRig()
	want := frame(3, yellow)
	r.k.Set(want)
	if r.dev.cur != want || r.dev.nwrites() != 1 {
		t.Fatalf("кадр не нарисован сразу: %+v, записей %d", r.dev.cur, r.dev.nwrites())
	}
	if st := r.k.Status(); !st.Owned || !st.Active {
		t.Fatalf("статус: %+v", st)
	}
	r.k.Set(want) // тот же кадр: повторной записи нет
	if r.dev.nwrites() != 1 {
		t.Fatal("тот же кадр записан повторно")
	}
	r.k.Set(frame(3, yellow).with(0, blue))
	if r.dev.nwrites() != 2 || r.dev.cur[0] != blue || r.dev.cur[3] != yellow {
		t.Fatalf("новый кадр не нарисован: %+v", r.dev.cur)
	}
}

func (f Frame) with(zone int, c led.Color) Frame { f[zone] = c; return f }

// Прошивка нарисовала анимацию Алисы поверх: уступаем, пока она не вернётся в покой и кадр не успокоится, потом возвращаем своё.
func TestYieldsToFirmwareAnimationAndTakesTheRingBack(t *testing.T) {
	r := newRig()
	want := frame(3, yellow)
	r.k.Set(want)
	writes := r.dev.nwrites()

	// Алиса слушает: прошивка пишет свой кадр и переключает анимацию
	r.an = Anim{Ring: []string{"alice_listening.led"}, At: r.now}
	r.dev.firmware(Frame{{R: 255}, {R: 255}, {R: 255}, {R: 255}, {R: 255}, {R: 255}})
	r.advance(50 * time.Millisecond)
	if st := r.k.Status(); st.Active || st.Yields != 1 {
		t.Fatalf("не уступили прошивке: %+v", st)
	}
	for i := 0; i < 20; i++ { // пока анимация идёт, свой кадр не пишем, как бы долго она ни шла
		r.advance(50 * time.Millisecond)
	}
	if r.dev.nwrites() != writes {
		t.Fatalf("писали поверх анимации прошивки: %d записей", r.dev.nwrites()-writes)
	}

	// анимация кончилась: idle.led, кольцо затухает (кадры меняются), и только потом гаснет
	r.an = Anim{Ring: []string{"idle.led"}, At: r.now}
	r.dev.firmware(frame(1, led.Color{R: 40}))
	r.advance(50 * time.Millisecond)
	r.dev.firmware(frame(1, led.Color{R: 10}))
	r.advance(50 * time.Millisecond)
	r.dev.firmware(Frame{})
	r.advance(50 * time.Millisecond)
	if r.dev.nwrites() != writes {
		t.Fatal("вернули своё, не дав затуханию кончиться")
	}
	r.advance(400 * time.Millisecond)
	if r.dev.cur != want || r.dev.nwrites() != writes+1 {
		t.Fatalf("свой кадр не возвращён: %+v, записей %d", r.dev.cur, r.dev.nwrites()-writes)
	}
	if st := r.k.Status(); !st.Active {
		t.Fatal("после возврата кадр не считается горящим")
	}
}

func TestSettingZeroFrameReleasesTheRing(t *testing.T) {
	r := newRig()
	r.k.Set(frame(0, blue))
	r.k.Set(Frame{})
	if r.dev.cur != (Frame{}) || r.k.Status().Owned {
		t.Fatalf("кольцо не отдано: %+v %+v", r.dev.cur, r.k.Status())
	}
	reads := r.dev.reads
	for i := 0; i < 10; i++ {
		r.advance(50 * time.Millisecond)
	}
	if r.dev.reads > reads+1 { // одно чтение — проверка, что кольцо погасло
		t.Fatalf("не нужное нам кольцо читается зря: %d чтений", r.dev.reads-reads)
	}
	r.k.Set(Frame{}) // повторное освобождение безвредно
}

func TestReleaseDoesNotTouchRingFirmwareIsDrawing(t *testing.T) {
	r := newRig()
	r.k.Set(frame(3, yellow))
	r.an = Anim{Ring: []string{"alice_thinking.led"}, At: r.now}
	anim := frame(1, led.Color{R: 9, G: 9, B: 9})
	r.dev.firmware(anim)
	r.advance(50 * time.Millisecond) // уступили
	writes := r.dev.nwrites()
	r.k.Release()
	if r.dev.nwrites() != writes || r.dev.cur != anim {
		t.Fatal("отпуская кольцо, затёрли анимацию прошивки")
	}
}

func TestWriteErrorsAreLoggedNotFatal(t *testing.T) {
	r := newRig()
	var logs int
	r.k.Logf = func(string, ...any) { logs++ }
	r.dev.fail = true
	r.k.Set(frame(3, yellow))
	if r.k.Status().Active {
		t.Fatal("кадр считается горящим после ошибки записи")
	}
	r.dev.fail = false
	r.advance(400 * time.Millisecond)
	if !r.k.Status().Active || r.dev.cur != frame(3, yellow) {
		t.Fatal("после ошибки запись не повторена")
	}
	if logs == 0 {
		t.Fatal("ошибку не записали в журнал")
	}
}

func TestRunReleasesOnCancel(t *testing.T) {
	r := newRig()
	r.k.Poll = 5 * time.Millisecond
	r.k.Now = time.Now
	r.k.Set(frame(3, yellow))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.k.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился")
	}
	if r.dev.cur != (Frame{}) || r.k.Status().Owned {
		t.Fatal("при остановке кольцо не отдано")
	}
}

// Анимация показывает и чёрные кадры, не отдавая кольцо: Show держит его, а Set с нулевым кадром отдал бы.
func TestShowKeepsTheRingOnBlackFrames(t *testing.T) {
	r := newRig()
	r.k.Show(frame(3, yellow))
	r.k.Show(Frame{})
	if !r.k.Status().Owned {
		t.Fatal("чёрный кадр анимации отдал кольцо")
	}
	if r.dev.cur != (Frame{}) {
		t.Fatalf("чёрный кадр не нарисован: %+v", r.dev.cur)
	}
	r.k.Show(frame(0, blue))
	if r.dev.cur != frame(0, blue) {
		t.Fatalf("следующий кадр не нарисован: %+v", r.dev.cur)
	}
	r.k.Release()
	if r.k.Status().Owned {
		t.Fatal("кольцо не отдано в конце")
	}
}

// Потерянная запись «погасить» не оставляет на кольце тусклый кадр навсегда: через 150 мс после отдачи его гасят снова.
func TestReleaseVerifiesTheRingWentDark(t *testing.T) {
	r := newRig()
	want := frame(3, yellow)
	r.k.Set(want)
	r.dev.dropZero = true
	r.k.Release()
	if r.dev.cur != want {
		t.Fatal("тест не воспроизводит потерянную запись")
	}
	r.advance(100 * time.Millisecond) // проверка ещё не наступила
	if r.dev.cur != want {
		t.Fatal("проверили слишком рано")
	}
	r.dev.mu.Lock()
	r.dev.dropZero = false
	r.dev.mu.Unlock()
	r.advance(100 * time.Millisecond)
	if !r.dev.cur.IsZero() {
		t.Fatalf("кольцо осталось гореть после отдачи: %+v", r.dev.cur)
	}
	n := r.dev.nwrites()
	r.advance(time.Second)
	if r.dev.nwrites() != n {
		t.Fatal("погасшее кольцо трогают снова")
	}
}

// Гасят заново не больше нескольких раз, чтобы не писать в кольцо вечно, если оно не гаснет.
func TestReleaseVerificationGivesUpAfterAFewTries(t *testing.T) {
	r := newRig()
	r.k.Set(frame(3, yellow))
	r.dev.dropZero = true
	r.k.Release()
	base := r.dev.nwrites()
	for i := 0; i < 20; i++ {
		r.advance(200 * time.Millisecond)
	}
	if got := r.dev.nwrites() - base; got != clearTries {
		t.Fatalf("повторных гашений %d, ждали %d", got, clearTries)
	}
}

// Если на кольце после отдачи уже не наш кадр, а то, что рисует прошивка, его не трогают.
func TestReleaseVerificationLeavesFirmwareFramesAlone(t *testing.T) {
	r := newRig()
	r.k.Set(frame(3, yellow))
	r.k.Release()
	fw := frame(1, blue)
	r.dev.firmware(fw)
	n := r.dev.nwrites()
	r.advance(300 * time.Millisecond)
	if r.dev.cur != fw || r.dev.nwrites() != n {
		t.Fatal("кадр прошивки затёрт проверкой гашения")
	}
}

// Новый кадр после отдачи отменяет проверку: кольцо снова наше.
func TestReleaseVerificationIsCancelledByANewFrame(t *testing.T) {
	r := newRig()
	r.k.Set(frame(3, yellow))
	r.k.Release()
	want := frame(0, blue)
	r.k.Set(want)
	r.advance(300 * time.Millisecond)
	if r.dev.cur != want {
		t.Fatalf("новый кадр затёрт: %+v", r.dev.cur)
	}
}

// Пока кольцо не нужно, Run просыпается редко (Idle); как только Set берёт кольцо, сверка снова идёт каждые Poll.
func TestRunWakesUpWhenTheRingIsNeeded(t *testing.T) {
	r := newRig()
	r.k.Poll, r.k.Idle = 5*time.Millisecond, time.Hour
	r.k.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.k.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	time.Sleep(20 * time.Millisecond) // Run уже ждёт Idle
	want := frame(3, yellow)
	r.k.Set(want)
	r.dev.firmware(Frame{})
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.dev.mu.Lock()
		back := r.dev.cur == want
		r.dev.mu.Unlock()
		if back {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Run не проснулся после Set: кадр не вернулся")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRingIdleAndParseLine(t *testing.T) {
	for ring, want := range map[string]bool{"": true, "idle.led": true, "alice_listening.led": false} {
		var a Anim
		if ring != "" {
			a.Ring = []string{ring}
		}
		if a.RingIdle() != want {
			t.Errorf("RingIdle(%q) = %v", ring, a.RingIdle())
		}
	}
	line := []byte(`[2026-10-31 23:39:00.551] [device_state_handler] [info] [device_state_handler.cpp:743] Play animations: [ /system/vendor/quasar/ledpatterns/oknix//alice_thinking.led idle.led ] [ spectrogram ] source=onSDKState`)
	a, ok := parseLine(line)
	if !ok || len(a.Ring) != 2 || a.Ring[0] != "alice_thinking.led" || !a.RingIdle() {
		t.Fatalf("parseLine: %+v %v", a, ok)
	}
	if _, ok := parseLine([]byte("Spectrum started. playerInstanceId: 5")); ok {
		t.Error("чужая строка принята")
	}
}

func TestWatcherFindsLastAnimationAtStartAndFollowsNewOnes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	write := func(s string, appendMode bool) {
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendMode {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(p, flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	line := func(name string) string {
		return "[x] [info] Play animations: [ /a/" + name + " ] [ spectrogram ] source=onSDKState\n"
	}
	write("шум\n"+line("alice_listening.led")+line("idle.led")+"хвост без перевода строки", false)
	w := &Watcher{Path: p}
	w.Poll(time.Unix(1, 0))
	if a := w.Current(); len(a.Ring) != 1 || a.Ring[0] != "idle.led" || !a.At.IsZero() {
		t.Fatalf("при старте нужно последнее переключение без метки времени: %+v", a)
	}
	write("\n"+line("alice_speaking.led"), true)
	w.Poll(time.Unix(2, 0))
	if a := w.Current(); a.Ring[0] != "alice_speaking.led" || !a.At.Equal(time.Unix(2, 0)) {
		t.Fatalf("новое переключение: %+v", a)
	}
	// ротация лога: файл стал короче
	write(line("idle.led"), false)
	w.Poll(time.Unix(3, 0))
	if a := w.Current(); a.Ring[0] != "idle.led" || !a.At.Equal(time.Unix(3, 0)) {
		t.Fatalf("после ротации: %+v", a)
	}
	// нет файла — не падаем
	(&Watcher{Path: filepath.Join(t.TempDir(), "нет")}).Poll(time.Now())
}

func TestFBReadWriteBGR(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fb0")
	if err := os.WriteFile(p, make([]byte, Zones*3), 0o644); err != nil {
		t.Fatal(err)
	}
	fb, err := OpenFB(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	want := frame(3, led.Color{R: 1, G: 2, B: 3})
	if err := fb.Write(want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if raw[9] != 3 || raw[10] != 2 || raw[11] != 1 { // зона 3: порядок байт B, G, R
		t.Fatalf("порядок байт в файле: % x", raw[9:12])
	}
	got, err := fb.Read()
	if err != nil || got != want {
		t.Fatalf("Read: %+v %v", got, err)
	}
	if _, err := OpenFB(filepath.Join(t.TempDir(), "нет")); err == nil {
		t.Fatal("несуществующее устройство открылось")
	}
}

// Если прошивка просто гасит кольцо (idle.led), ждать затухания незачем: вернуть кадр только через 300 мс значило бы мигнуть тёмным.
func TestReclaimsQuicklyWhenFirmwareJustBlanksTheRing(t *testing.T) {
	r := newRig()
	want := frame(3, yellow)
	r.k.Set(want)
	writes := r.dev.nwrites()
	r.dev.firmware(Frame{}) // idle.led: кольцо погашено, лог прошивки не меняется
	r.advance(10 * time.Millisecond)
	if r.dev.nwrites() != writes {
		t.Fatal("вернули кадр в ту же миллисекунду, не убедившись, что прошивка закончила")
	}
	r.advance(30 * time.Millisecond)
	if r.dev.cur != want || r.dev.nwrites() != writes+1 {
		t.Fatalf("кадр не вернулся за 40 мс: %+v, записей %d", r.dev.cur, r.dev.nwrites()-writes)
	}
	// и так каждую секунду
	for i := 0; i < 3; i++ {
		r.advance(time.Second)
		r.dev.firmware(Frame{})
		r.advance(10 * time.Millisecond)
		r.advance(30 * time.Millisecond)
		if r.dev.cur != want {
			t.Fatalf("гашение №%d: кадр не вернулся", i+2)
		}
	}
}

// Пока прошивка играет анимацию Алисы, быстрый возврат не действует, даже если в какой-то миг кадр оказался чёрным.
func TestQuickReclaimNeverInterruptsAnAnimation(t *testing.T) {
	r := newRig()
	r.k.Set(frame(3, yellow))
	writes := r.dev.nwrites()
	r.an = Anim{Ring: []string{"alice_thinking.led"}, At: r.now}
	r.dev.firmware(Frame{}) // чёрный кадр посреди анимации
	for i := 0; i < 10; i++ {
		r.advance(20 * time.Millisecond)
	}
	if r.dev.nwrites() != writes {
		t.Fatal("писали посреди анимации прошивки")
	}
}

// Вторая пара скобок в строке лога — анимация дисплея с часами: свободен он только когда показывает время.
func TestAnimClockPartOfTheLog(t *testing.T) {
	for line, want := range map[string]struct {
		clock []string
		idle  bool
	}{
		`Play animations: [ idle.led ] [ <Animation> ] source=onSDKState`:                                                {[]string{"<Animation>"}, true},
		`Play animations: [ idle.led ] [ spectrogram ] source=onSpectrumChanged`:                                         {[]string{"spectrogram"}, false},
		`Play animations: [ /x/oknix//alice_thinking.led ] [ thinkingWithDelay ] source=onSDKState`:                      {[]string{"thinkingWithDelay"}, false},
		`Play animations: [ /x/oknix//alice_listening.led ] [ /x/clock/alice_listening.binscled.zst ] source=onSDKState`: {[]string{"alice_listening.binscled.zst"}, false},
	} {
		a, ok := parseLine([]byte(line))
		if !ok {
			t.Fatalf("%q не разобрана", line)
		}
		if len(a.Clock) != len(want.clock) || a.Clock[0] != want.clock[0] || a.ClockIdle() != want.idle {
			t.Errorf("%q → дисплей %v свободен=%v, ждали %v %v", line, a.Clock, a.ClockIdle(), want.clock, want.idle)
		}
	}
	if !(Anim{}).ClockIdle() {
		t.Error("прошивка ничего не сообщала — дисплей считается свободным")
	}
}
