package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/system/bluez"
	"openmediad/internal/system/dbus"
)

const hp = "C0:DA:5E:53:03:B1"

func hpPath() dbus.ObjectPath { return "/org/bluez/hci0/dev_C0_DA_5E_53_03_B1" }

// fakeClient — Bluetooth в памяти: состояние меняется тестом, изменения уходят в канал Watch.
type fakeClient struct {
	mu       sync.Mutex
	state    bluez.State
	calls    []string
	changes  chan struct{}
	closed   bool
	connects int
	onConn   func(*fakeClient)
}

func newFakeClient() *fakeClient {
	return &fakeClient{changes: make(chan struct{}, 4), state: bluez.State{
		Adapters: []bluez.Adapter{{Path: "/org/bluez/hci0", Powered: true}},
		Devices: []bluez.Device{{Path: hpPath(), Address: hp, Name: "FreeBuds", Paired: true, Trusted: true,
			UUIDs: []string{bluez.UUIDAudioSink}}},
	}}
}

// connect — наушники подключились: устройство Connected, PCM вывода и транспорт A2DP (пока простаивает) появились.
func (f *fakeClient) connect(addr string, path dbus.ObjectPath) {
	f.mu.Lock()
	for i := range f.state.Devices {
		if f.state.Devices[i].Address == addr {
			f.state.Devices[i].Connected = true
		}
	}
	f.state.PCMs = append(f.state.PCMs, bluez.PCM{Path: dbus.ObjectPath(string(path) + "/a2dpsrc/sink"), Device: path, Address: addr,
		Transport: "A2DP-source", Mode: "sink", Sampling: 48000, Channels: 2})
	f.state.Transports = append(f.state.Transports, bluez.Transport{Path: dbus.ObjectPath(string(path) + "/sep1/fd7"), Device: path,
		Address: addr, UUID: "0000110A-0000-1000-8000-00805F9B34FB", State: "idle"})
	f.mu.Unlock()
	f.changes <- struct{}{}
}

// transport меняет состояние транспорта (как сигнал BlueZ).
func (f *fakeClient) transport(path dbus.ObjectPath, state string) {
	f.mu.Lock()
	for i := range f.state.Transports {
		if f.state.Transports[i].Path == path {
			f.state.Transports[i].State = state
		}
	}
	f.mu.Unlock()
	f.changes <- struct{}{}
}

func (f *fakeClient) disconnect() {
	f.mu.Lock()
	f.state.PCMs = nil
	f.state.Transports = nil
	for i := range f.state.Devices {
		f.state.Devices[i].Connected = false
	}
	f.mu.Unlock()
	f.changes <- struct{}{}
}

func (f *fakeClient) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

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
	s := f.state
	s.Devices = append([]bluez.Device(nil), f.state.Devices...)
	s.PCMs = append([]bluez.PCM(nil), f.state.PCMs...)
	s.Transports = append([]bluez.Transport(nil), f.state.Transports...)
	return s, nil
}
func (f *fakeClient) Connections(_ context.Context, pid int) ([]string, error) {
	switch pid {
	case 1907:
		return []string{":1.5", ":1.6"}, nil
	case 2000: // платформа после перезапуска
		return []string{":1.9"}, nil
	}
	return nil, nil
}
func (f *fakeClient) HideTransport(dests []string, path dbus.ObjectPath) error {
	f.rec(fmt.Sprintf("hide %s %v", path, dests))
	return nil
}
func (f *fakeClient) ForgetTransport(dests []string, path dbus.ObjectPath) error {
	f.rec(fmt.Sprintf("forget %s %v", path, dests))
	return nil
}

func (f *fakeClient) index(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}
func (f *fakeClient) Watch(context.Context) (<-chan struct{}, error) { return f.changes, nil }
func (f *fakeClient) Connect(_ context.Context, dev dbus.ObjectPath) error {
	f.rec("connect " + string(dev))
	f.mu.Lock()
	f.connects++
	cb := f.onConn
	f.mu.Unlock()
	if cb != nil {
		cb(f)
		return nil
	}
	return errors.New("Page Timeout")
}
func (f *fakeClient) SetPCMVolume(_ context.Context, pcm dbus.ObjectPath, level byte) error {
	f.rec(fmt.Sprintf("volume %d", level))
	return nil
}
func (f *fakeClient) SetPCMSoftVolume(_ context.Context, pcm dbus.ObjectPath, on bool) error {
	f.rec(fmt.Sprintf("soft %v", on))
	return nil
}
func (f *fakeClient) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

type fakeMaster struct {
	mu      sync.Mutex
	cur     int
	changes chan struct{}
	closed  bool
}

func (m *fakeMaster) Level() (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, 255, !m.closed
}
func (m *fakeMaster) Changes() <-chan struct{} { return m.changes }
func (m *fakeMaster) Close()                   { m.mu.Lock(); m.closed = true; m.mu.Unlock() }
func (m *fakeMaster) set(v int) {
	m.mu.Lock()
	m.cur = v
	m.mu.Unlock()
	m.changes <- struct{}{}
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

// memOut — вывод в наушники в памяти (вместо aplay): копит записанное, «играет» с небольшой задержкой на запись.
// hold задерживает запись (как заполненный буфер aplay), blocked — сколько записей сейчас ждут.
type memOut struct {
	mu      sync.Mutex
	buf     []byte
	aborted bool
	failAt  int
	hold    sync.Mutex
	blocked atomic.Int32
}

func (o *memOut) Write(p []byte) (int, error) {
	o.blocked.Add(1)
	o.hold.Lock()
	o.hold.Unlock()
	o.blocked.Add(-1)
	time.Sleep(time.Millisecond)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.aborted || o.failAt > 0 && len(o.buf) >= o.failAt {
		return 0, errors.New("aplay вышел")
	}
	o.buf = append(o.buf, p...)
	return len(p), nil
}
func (o *memOut) Drain() {}
func (o *memOut) Abort() { o.mu.Lock(); o.aborted = true; o.mu.Unlock() }
func (o *memOut) data() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buf...)
}

func start(t *testing.T, s *Sink, fc *fakeClient, m *fakeMaster) {
	s.Dial = func() (Client, error) { return fc, nil }
	if s.OpenOutput == nil {
		s.OpenOutput = func(string) (audio.Output, error) { return &memOut{}, nil }
	}
	if s.PlatformPID == nil {
		s.PlatformPID = func() int { return 1907 }
	}
	if m != nil {
		s.OpenMaster = func() (Master, error) { return m, nil }
	}
	s.Logf = func(f string, a ...any) { t.Logf(f, a...) }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
}

func TestAvailableFollowsHeadphones(t *testing.T) {
	fc := newFakeClient()
	s := &Sink{}
	start(t, s, fc, nil)
	if s.Available() {
		t.Fatal("наушники не подключены — выход недоступен")
	}
	if _, err := s.Open(audio.Stereo48); err == nil {
		t.Fatal("Open без наушников — ошибка")
	}
	fc.connect(hp, hpPath())
	waitFor(t, "выход доступен", s.Available)
	if st := s.DebugState().(State); !st.Connected || st.Device != hp || st.Name != "FreeBuds" || st.Rate != 48000 {
		t.Fatalf("состояние %+v", st)
	}
	fc.disconnect()
	waitFor(t, "выход недоступен", func() bool { return !s.Available() })
}

// Выбранные наушники — только они: другие подключённые устройства выход не делают доступным.
func TestDeviceFilter(t *testing.T) {
	fc := newFakeClient()
	s := &Sink{Device: "11:22:33:44:55:66"}
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	time.Sleep(30 * time.Millisecond)
	if s.Available() {
		t.Fatal("подключены чужие наушники — выход недоступен")
	}
}

// Громкость наушников повторяет громкость колонки: при подключении BlueALSA переходит на свою громкость, дальше —
// каждое изменение Master.
func TestVolumeFollowsSpeaker(t *testing.T) {
	fc := newFakeClient()
	m := &fakeMaster{cur: 46, changes: make(chan struct{}, 1)}
	s := &Sink{Volume: VolumeSpeaker}
	start(t, s, fc, m)
	fc.connect(hp, hpPath())
	waitFor(t, "громкость при подключении", func() bool { return fc.has("volume 13") == 1 }) // Master 46 = уровень 2 из 20
	if fc.has("soft true") != 1 {
		t.Fatalf("BlueALSA должна взять громкость на себя: %v", fc.calls)
	}
	m.set(68)
	// Master 68 = уровень 4
	waitFor(t, "громкость после изменения", func() bool { return fc.has("volume 25") == 1 })
	m.set(68) // то же значение — лишнего вызова нет
	time.Sleep(30 * time.Millisecond)
	if n := fc.has("volume "); n != 2 {
		t.Fatalf("вызовов громкости %d, ждали 2: %v", n, fc.calls)
	}
	s.Stop()
	if !m.closed {
		t.Fatal("слежение за громкостью закрывается при остановке")
	}
}

func TestVolumeHeadphonesModeLeavesVolumeAlone(t *testing.T) {
	fc := newFakeClient()
	m := &fakeMaster{cur: 46, changes: make(chan struct{}, 1)}
	opened := false
	s := &Sink{Volume: VolumeHeadphones}
	s.OpenMaster = func() (Master, error) { opened = true; return m, nil }
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	waitFor(t, "выход доступен", s.Available)
	time.Sleep(20 * time.Millisecond)
	if opened || fc.has("volume") != 0 || fc.has("soft") != 0 {
		t.Fatalf("в режиме headphones громкость не трогается: %v", fc.calls)
	}
}

// Шкала платформы: значения Master — как в её журнале и amixer (уровень 12 → 162, 13 → 173, 14 → 185). Главное — круг
// «уровень → Master → громкость наушников → уровень (как пересчитывает платформа)» на каждом уровне возвращается туда же:
// иначе громкость уезжает сама (линейный пересчёт тянул всё к 14 из 20).
func TestVolumeMap(t *testing.T) {
	m := VolumeMap{} // пустая — шкала bergamot
	for lvl, want := range map[int]int{0: 0, 1: 33, 2: 45, 4: 68, 12: 162, 13: 173, 14: 185, 20: 255} {
		if got := m.Master(lvl); got != want {
			t.Errorf("Master(%d) = %d, ждали %d", lvl, got, want)
		}
	}
	for lvl := 0; lvl <= 20; lvl++ {
		hp := m.Headphones(m.Master(lvl))
		back := int(math.Round(float64(hp) * 20 / 127)) // так платформа читает громкость Bluetooth
		if m.Level(m.Master(lvl)) != lvl || back != lvl {
			t.Errorf("уровень %d → Master %d → наушники %d → платформа %d", lvl, m.Master(lvl), hp, back)
		}
	}
	if m.Headphones(46) != 13 || m.Headphones(68) != 25 { // как платформа ставит телефону: уровень 2 → 13, 4 → 25
		t.Errorf("Headphones(46) = %d, Headphones(68) = %d", m.Headphones(46), m.Headphones(68))
	}
	if m.Headphones(0) != 0 || m.Headphones(255) != 127 || m.Headphones(300) != 127 {
		t.Error("края шкалы")
	}
}

func TestLoadVolumeMap(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "quasar.cfg")
	os.WriteFile(cfg, []byte(`{"volume_capability": {"volume_manager_settings": {"platformToAlsaMap": "{ \"0\": 0, \"1\" : 33, \"20\": 255}"}}, "x": [1.5]}`), 0o644)
	m, err := LoadVolumeMap(cfg)
	if err != nil || m.Master(14) != 185 || m.Levels() != 20 {
		t.Fatalf("%v %+v", err, m)
	}
	os.WriteFile(cfg, []byte(`{"volume_capability": {"volume_manager_settings": {"platformToAlsaMap": "{\"0\": 0, \"10\": 200}"}}}`), 0o644)
	if m, _ := LoadVolumeMap(cfg); m.Levels() != 10 || m.Master(5) != 100 {
		t.Fatalf("своя шкала: %+v", m)
	}
	for _, bad := range []string{`{}`, `{"volume_capability": {"volume_manager_settings": {"platformToAlsaMap": "{\"5\": 1}"}}}`, `не json`} {
		os.WriteFile(cfg, []byte(bad), 0o644)
		if m, err := LoadVolumeMap(cfg); err == nil || m.Master(14) != 185 {
			t.Errorf("%s: ждали ошибку и шкалу bergamot", bad)
		}
	}
	if _, err := LoadVolumeMap(filepath.Join(dir, "нет")); err == nil {
		t.Error("нет файла — ошибка")
	}
}

// Выбранные наушники не подключены: колонка сама пробует их подключить (когда Bluetooth «выключен», они не могут сами).
func TestReconnectsChosenHeadphones(t *testing.T) {
	fc := newFakeClient()
	fc.onConn = nil // первая попытка — наушники выключены
	s := &Sink{Device: hp, Reconnect: 20 * time.Millisecond}
	start(t, s, fc, nil)
	waitFor(t, "первая попытка", func() bool { return fc.has("connect ") >= 1 })
	fc.mu.Lock()
	fc.onConn = func(f *fakeClient) { go f.connect(hp, hpPath()) }
	fc.mu.Unlock()
	waitFor(t, "подключились", s.Available)
	n := fc.has("connect ")
	time.Sleep(80 * time.Millisecond)
	if fc.has("connect ") != n {
		t.Fatal("подключённые наушники больше не подключают")
	}
}

func TestNoReconnectWithoutChosenDevice(t *testing.T) {
	fc := newFakeClient()
	s := &Sink{Reconnect: 10 * time.Millisecond}
	start(t, s, fc, nil)
	time.Sleep(60 * time.Millisecond)
	if fc.has("connect") != 0 {
		t.Fatal("без выбранных наушников колонка никого не подключает сама")
	}
}

// Шина недоступна (BlueZ перезапускается) — выход пробует снова и потом работает.
func TestRetriesBus(t *testing.T) {
	fc := newFakeClient()
	fc.connect(hp, hpPath())
	var mu sync.Mutex
	tries := 0
	s := &Sink{}
	s.Dial = func() (Client, error) {
		mu.Lock()
		defer mu.Unlock()
		tries++
		if tries == 1 {
			return nil, errors.New("нет шины")
		}
		return fc, nil
	}
	s.Logf = func(f string, a ...any) { t.Logf(f, a...) }
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "после повтора выход доступен", s.Available)
}

// Канал в наушники открывается при подключении и закрывается при отключении; потоки движка идут в него.
func TestChannelFollowsHeadphones(t *testing.T) {
	fc := newFakeClient()
	var mu sync.Mutex
	var outs []*memOut
	s := &Sink{OpenOutput: func(addr string) (audio.Output, error) {
		mu.Lock()
		defer mu.Unlock()
		o := &memOut{}
		outs = append(outs, o)
		return o, nil
	}}
	var arrived atomic.Int32
	s.OnArrival(func() { arrived.Add(1) })
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	waitFor(t, "канал открыт", func() bool { return s.DebugState().(State).Channel })
	waitFor(t, "о подключении сообщено", func() bool { return arrived.Load() == 1 })
	fc.changes <- struct{}{} // те же наушники — повторно не сообщается
	out, err := s.Open(audio.Format{Rate: 48000, Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write(make([]byte, 960)); err != nil {
		t.Fatal(err)
	}
	if st := s.DebugState().(State); st.Streams != 1 {
		t.Fatalf("потоков %d", st.Streams)
	}
	out.Abort()
	if _, err := s.Open(audio.Format{Rate: 44100, Channels: 2}); err == nil {
		t.Fatal("44,1 кГц смеситель не принимает (движок пишет 48 кГц)")
	}
	fc.disconnect()
	waitFor(t, "канал закрыт", func() bool { return !s.DebugState().(State).Channel })
	waitFor(t, "вывод оборван", func() bool {
		mu.Lock()
		defer mu.Unlock()
		outs[0].mu.Lock()
		defer outs[0].mu.Unlock()
		return outs[0].aborted
	})
	mu.Lock()
	n := len(outs)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("выводов открыто %d, ждали 1", n)
	}
	if arrived.Load() != 1 {
		t.Fatalf("о подключении сообщено %d раз", arrived.Load())
	}
}

// Транспорт наушников платформе (всем её соединениям) адресно «удаляется», как только появился, — раньше, чем открывается
// канал, который его включит; тогда платформа его включения не видит. Один раз на путь; телефон не трогается.
// Переподключение (тот же путь) и перезапуск платформы — заново; уже активный транспорт сначала «простаивает».
func TestHidesHeadphoneTransportFromPlatform(t *testing.T) {
	fc := newFakeClient()
	fc.state.Transports = append(fc.state.Transports, bluez.Transport{Path: "/org/bluez/hci0/dev_CC/sep2/fd1", Device: "/org/bluez/hci0/dev_CC",
		UUID: bluez.UUIDAudioSink, State: "active"}) // телефон играет
	var pid atomic.Int32
	pid.Store(1907)
	s := &Sink{
		PlatformPID: func() int { return int(pid.Load()) },
		OpenOutput:  func(addr string) (audio.Output, error) { fc.rec("open " + addr); return &memOut{}, nil },
	}
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	tr := dbus.ObjectPath(string(hpPath()) + "/sep1/fd7")
	waitFor(t, "забыт", func() bool { return fc.has("forget "+string(tr)+" [:1.5 :1.6]") == 1 })
	waitFor(t, "канал открыт", func() bool { return fc.has("open ") == 1 })
	if f, o := fc.index("forget"), fc.index("open"); f > o {
		t.Fatalf("платформа должна забыть транспорт раньше, чем канал его включит: %v", fc.calls)
	}
	if fc.has("hide") != 0 || fc.has("forget /org/bluez/hci0/dev_CC") != 0 {
		t.Fatalf("простаивающему — без «простаивает», телефон не трогается: %v", fc.calls)
	}
	fc.transport(tr, "active") // канал включил транспорт — платформа его уже не знает, повторять нечего
	fc.changes <- struct{}{}
	time.Sleep(30 * time.Millisecond)
	if fc.has("forget") != 1 || fc.has("hide") != 0 {
		t.Fatalf("сообщено лишнее: %v", fc.calls)
	}
	// переподключение: тот же путь транспорта появляется снова — платформе сообщается заново
	fc.disconnect()
	waitFor(t, "отключены", func() bool { return !s.Available() })
	fc.connect(hp, hpPath())
	waitFor(t, "забыт после переподключения", func() bool { return fc.has("forget") == 2 })
	// платформа перезапустилась и перечитала объекты; транспорт уже активен — сначала «простаивает», потом «удалён»
	fc.transport(tr, "active")
	pid.Store(2000)
	fc.changes <- struct{}{}
	waitFor(t, "забыт новой платформой", func() bool { return fc.has("forget "+string(tr)+" [:1.9]") == 1 })
	if fc.has("hide "+string(tr)+" [:1.9]") != 1 || fc.index("hide") > fc.index("forget "+string(tr)+" [:1.9]") {
		t.Fatalf("активный транспорт: сначала «простаивает»: %v", fc.calls)
	}
	if st := s.DebugState().(State); st.Hidden != 3 {
		t.Fatalf("счётчик %d", st.Hidden)
	}
}

func TestNoHideWithoutPlatform(t *testing.T) {
	fc := newFakeClient()
	s := &Sink{PlatformPID: func() int { return 0 }}
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	fc.transport(dbus.ObjectPath(string(hpPath())+"/sep1/fd7"), "active")
	time.Sleep(50 * time.Millisecond)
	if fc.has("hide") != 0 || fc.has("forget") != 0 {
		t.Fatal("платформы нет — сообщать некому")
	}
}

func TestALSADevice(t *testing.T) {
	if d := bluez.ALSADevice(hp); d != "plug:'bluealsa:DEV=C0:DA:5E:53:03:B1,PROFILE=a2dp'" {
		t.Fatalf("устройство %q", d)
	}
}
