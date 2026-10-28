package btin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/engine"
	"openmediad/internal/system/bluez"
	"openmediad/internal/system/dbus"
)

const phone = "CC:FF:90:DD:63:86"

func phonePath() dbus.ObjectPath { return "/org/bluealsa/hci0/dev_CC_FF_90_DD_63_86/a2dpsnk/source" }

// fakeClient — BlueALSA в памяти: PCM телефона меняет тест, изменения уходят в канал Watch.
type fakeClient struct {
	mu      sync.Mutex
	pcm     *bluez.PCM
	calls   []string
	changes chan struct{}
}

func newFakeClient() *fakeClient { return &fakeClient{changes: make(chan struct{}, 8)} }

func (f *fakeClient) set(fn func(p *bluez.PCM)) {
	f.mu.Lock()
	if f.pcm == nil {
		f.pcm = &bluez.PCM{Path: phonePath(), Address: phone, Transport: "A2DP-sink", Mode: "source", Sampling: 44100, Channels: 2,
			Codec: "SBC", Volume: 13<<8 | 13}
	}
	fn(f.pcm)
	f.mu.Unlock()
	f.changes <- struct{}{}
}

func (f *fakeClient) gone() {
	f.mu.Lock()
	f.pcm = nil
	f.mu.Unlock()
	f.changes <- struct{}{}
}

func (f *fakeClient) has(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeClient) State(context.Context) (bluez.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var st bluez.State
	// наушники (звук в устройство) — не телефон, их PCM источник не трогает
	st.PCMs = append(st.PCMs, bluez.PCM{Path: "/org/bluealsa/hci0/dev_A0/a2dpsrc/sink", Address: "A0:AD:9F:4D:40:5E",
		Transport: "A2DP-source", Mode: "sink", Running: true, Sampling: 48000, Channels: 2})
	if f.pcm != nil {
		st.PCMs = append(st.PCMs, *f.pcm)
	}
	return st, nil
}
func (f *fakeClient) Watch(context.Context) (<-chan struct{}, error) { return f.changes, nil }
func (f *fakeClient) SetPCMSoftVolume(_ context.Context, pcm dbus.ObjectPath, on bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("soft %s %v", pcm, on))
	if f.pcm != nil && f.pcm.Path == pcm {
		f.pcm.SoftVolume = on
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) Close() {}

type fakeAplay struct {
	mu      sync.Mutex
	running bool
	calls   []string
}

func (a *fakeAplay) Running() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.running }
func (a *fakeAplay) Start() error {
	a.mu.Lock()
	a.running = true
	a.calls = append(a.calls, "start")
	a.mu.Unlock()
	return nil
}
func (a *fakeAplay) Stop() error {
	a.mu.Lock()
	a.running = false
	a.calls = append(a.calls, "stop")
	a.mu.Unlock()
	return nil
}

// memOut — вывод движка в память.
type memOut struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	aborted bool
}

func (o *memOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.aborted {
		return 0, io.ErrClosedPipe
	}
	return o.buf.Write(p)
}
func (o *memOut) Drain() {}
func (o *memOut) Abort() { o.mu.Lock(); o.aborted = true; o.mu.Unlock() }
func (o *memOut) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Len()
}

// rig — источник с движком в память и PCM телефона из трубы.
type rig struct {
	src    *Source
	fc     *fakeClient
	aplay  *fakeAplay
	mu     sync.Mutex
	outs   []*memOut
	pipes  []*io.PipeWriter
	opened []string
}

func newRig(t *testing.T) *rig {
	r := &rig{fc: newFakeClient(), aplay: &fakeAplay{running: true}}
	eng := &engine.Engine{Router: engine.RouterFunc(func(c engine.Class, f audio.Format) (audio.Output, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		o := &memOut{}
		r.outs = append(r.outs, o)
		return o, nil
	})}
	r.src = &Source{
		Engine: eng,
		Gain:   0.5,
		Logf:   func(f string, a ...any) { t.Logf(f, a...) },
		Dial:   func() (Client, error) { return r.fc, nil },
		Aplay:  r.aplay,
		OpenPCM: func(addr string, f audio.Format) (io.ReadCloser, error) {
			pr, pw := io.Pipe()
			r.mu.Lock()
			r.pipes = append(r.pipes, pw)
			r.opened = append(r.opened, fmt.Sprintf("%s %d×%d", addr, f.Rate, f.Channels))
			r.mu.Unlock()
			return pr, nil
		},
	}
	if err := r.src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.src.Stop)
	return r
}

func (r *rig) pipe(i int) *io.PipeWriter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pipes[i]
}

func (r *rig) count() (outs, pipes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outs), len(r.pipes)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

func state(s *Source) State { return s.DebugState().(State) }

// Штатный bluealsa-aplay на время работы остановлен (PCM телефона принимает одного клиента) и после — запущен снова.
func TestAplayStoppedAndRestored(t *testing.T) {
	r := newRig(t)
	if r.aplay.Running() || !state(r.src).AplayOff {
		t.Fatal("bluealsa-aplay должен быть остановлен")
	}
	r.src.Stop()
	if !r.aplay.Running() || strings.Join(r.aplay.calls, ",") != "stop,start" {
		t.Fatalf("после остановки bluealsa-aplay снова работает: %v", r.aplay.calls)
	}
}

// Прошлый запуск демона оборвался без Stop и оставил bluealsa-aplay остановленным: этот запуск вернёт его при остановке.
func TestAplayRestoredEvenIfAlreadyStopped(t *testing.T) {
	r := newRig(t)
	r.src.Stop()
	r.aplay.mu.Lock()
	r.aplay.running, r.aplay.calls = false, nil
	r.aplay.mu.Unlock()
	s := &Source{Engine: r.src.Engine, Dial: r.src.Dial, Aplay: r.aplay, OpenPCM: r.src.OpenPCM}
	s.Start(context.Background())
	s.Stop()
	if strings.Join(r.aplay.calls, ",") != "start" {
		t.Fatalf("службу запускаем, хотя останавливали не мы: %v", r.aplay.calls)
	}
}

// Телефон подключился — ничего не открыто; заиграл — поток движка читает PCM и играет; перестал — поток закрыт;
// заиграл снова — новый поток.
func TestStreamFollowsPhone(t *testing.T) {
	r := newRig(t)
	r.fc.set(func(p *bluez.PCM) {})
	waitFor(t, "телефон виден", func() bool { return state(r.src).Phone == phone })
	if _, pipes := r.count(); pipes != 0 || state(r.src).Streaming {
		t.Fatal("телефон не играет — PCM не открывается")
	}
	r.fc.set(func(p *bluez.PCM) { p.Running = true })
	waitFor(t, "поток открыт", func() bool { _, p := r.count(); return p == 1 })
	r.mu.Lock()
	if r.opened[0] != phone+" 44100×2" {
		t.Fatalf("PCM открыт как %q", r.opened[0])
	}
	r.mu.Unlock()
	go r.pipe(0).Write(make([]byte, 44100*4/5)) // 200 мс звука
	waitFor(t, "звук дошёл до вывода", func() bool { o, _ := r.count(); return o == 1 && r.outs[0].len() > 0 })
	if !state(r.src).Streaming {
		t.Fatal("состояние: играет через движок")
	}
	r.fc.set(func(p *bluez.PCM) { p.Running = false })
	waitFor(t, "поток закрыт", func() bool { return !state(r.src).Streaming })
	if _, err := r.pipe(0).Write([]byte{0, 0, 0, 0}); err == nil {
		t.Fatal("чтение PCM закрыто вместе с потоком")
	}
	r.fc.set(func(p *bluez.PCM) { p.Running = true })
	waitFor(t, "новый поток", func() bool { _, p := r.count(); return p == 2 })
	r.fc.gone()
	waitFor(t, "телефон отключился — поток закрыт", func() bool { st := state(r.src); return !st.Streaming && st.Phone == "" })
}

// Платформа приглушает телефон на время диалога программной громкостью BlueALSA — источник её выключает (приглушает
// движок); громкость, которую BlueALSA передаёт телефону, не трогается.
func TestUnducksPlatformSoftVolume(t *testing.T) {
	r := newRig(t)
	r.fc.set(func(p *bluez.PCM) { p.Running = true })
	time.Sleep(30 * time.Millisecond)
	if r.fc.has("soft") != 0 {
		t.Fatal("без приглушения платформы громкость не трогаем")
	}
	r.fc.set(func(p *bluez.PCM) { p.SoftVolume, p.Volume = true, 21<<8|21 })
	waitFor(t, "приглушение снято", func() bool { return r.fc.has("soft "+string(phonePath())+" false") == 1 })
	r.fc.changes <- struct{}{}
	time.Sleep(30 * time.Millisecond)
	if r.fc.has("soft") != 1 || state(r.src).Unducked != 1 {
		t.Fatalf("уже снято — повторно не ставим: %v", r.fc.calls)
	}
	r.fc.mu.Lock()
	vol := r.fc.pcm.Volume
	r.fc.mu.Unlock()
	if vol != 21<<8|21 {
		t.Fatalf("громкость телефона тронута: %x", vol)
	}
}

// arecord оборвался, а телефон играет — поток открывается заново, но не чаще чем раз в 2 с.
func TestStreamReopensAfterBreak(t *testing.T) {
	r := newRig(t)
	r.fc.set(func(p *bluez.PCM) { p.Running = true })
	waitFor(t, "поток открыт", func() bool { _, p := r.count(); return p == 1 })
	go r.pipe(0).Write(make([]byte, 4410*4))
	waitFor(t, "звук пошёл", func() bool { o, _ := r.count(); return o == 1 && r.outs[0].len() > 0 })
	r.pipe(0).Close() // arecord вышел
	waitFor(t, "поток кончился", func() bool { return !state(r.src).Streaming })
	time.Sleep(200 * time.Millisecond)
	if _, p := r.count(); p != 1 {
		t.Fatal("сразу не переоткрываем")
	}
	waitFor2 := func() bool { _, p := r.count(); return p == 2 }
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && !waitFor2(); time.Sleep(10 * time.Millisecond) {
	}
	if !waitFor2() {
		t.Fatal("через 2 с поток открыт заново")
	}
}

// Read отдаёт только целые кадры: громкость и спектр движка считают по отсчётам.
func TestPCMSourceWholeFrames(t *testing.T) {
	pr, pw := io.Pipe()
	s := &pcmSource{f: audio.Format{Rate: 44100, Channels: 2}, open: func() (io.ReadCloser, error) { return pr, nil }}
	s.Open(context.Background())
	go func() { pw.Write([]byte{1, 2, 3, 4, 5, 6, 7}); pw.Close() }()
	buf := make([]byte, 10)
	n, err := s.Read(buf)
	if n != 4 || err != nil {
		t.Fatalf("прочитано %d (%v), ждали целый кадр (4 байта)", n, err)
	}
	if _, err := s.Read(buf); err != io.EOF {
		t.Fatalf("после конца — EOF: %v", err)
	}
}

// stuckAplay — служба, которая не останавливается.
type stuckAplay struct{ fakeAplay }

func (a *stuckAplay) Stop() error { return nil }

// bluealsa-aplay не остановился — источник не запускается (PCM телефона занят), а не делает вид, что всё в порядке.
func TestStartFailsIfAplayKeepsRunning(t *testing.T) {
	a := &stuckAplay{fakeAplay{running: true}}
	s := &Source{Engine: &engine.Engine{Router: engine.RouterFunc(nil)}, Dial: func() (Client, error) { return newFakeClient(), nil }, Aplay: a}
	if err := s.Start(context.Background()); err == nil {
		s.Stop()
		t.Fatal("ждали ошибку запуска")
	}
}

// slowAplay — служба, которая после сигнала выходит не сразу (как настоящий bluealsa-aplay после скрипта init.d).
type slowAplay struct {
	fakeAplay
	exitAt time.Time
}

func (a *slowAplay) Stop() error { a.exitAt = time.Now().Add(150 * time.Millisecond); return nil }
func (a *slowAplay) Running() bool {
	return a.exitAt.IsZero() || time.Now().Before(a.exitAt)
}

func TestStartWaitsForAplayToExit(t *testing.T) {
	a := &slowAplay{fakeAplay: fakeAplay{running: true}}
	s := &Source{Engine: &engine.Engine{Router: engine.RouterFunc(nil)}, Dial: func() (Client, error) { return newFakeClient(), nil }, Aplay: a}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("служба вышла за 150 мс — запуск успешен: %v", err)
	}
	s.Aplay = nil
	s.Stop()
}
