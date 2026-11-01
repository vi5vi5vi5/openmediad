package spotifysoloist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/libs/volmap"
)

// Сквозная проверка компонента на поддельном Soloist: «загрузчик» — копия самого тестового бинарника, которая по переменной
// окружения OMD_FAKE_SOLOIST ведёт себя как Soloist (пишет журнал, держит WebSocket, шлёт звук в сокет, как шим).

func TestMain(m *testing.M) {
	if os.Getenv("OMD_FAKE_SOLOIST") == "1" {
		fakeSoloist()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeSoloist() {
	var ws, key, vol string
	args := os.Args[1:]
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "-w":
			ws = args[i+1]
		case "-k":
			key = args[i+1]
		case "-i":
			vol = args[i+1]
		}
	}
	if vol != "50" || os.Getenv("OMD_SHIM_VOLUME") != "off" {
		fmt.Fprintf(os.Stderr, "фальшивый Soloist: громкость старта %q и режим шима %q — ждали 50 (колонка на 50%%) и off\n", vol, os.Getenv("OMD_SHIM_VOLUME"))
		os.Exit(7)
	}
	if key != "spak_testkey_1234567890" || ws == "" {
		fmt.Fprintln(os.Stderr, "фальшивый Soloist: ключ или -w не переданы")
		os.Exit(3)
	}
	srv, err := listenWS(ws)
	if err != nil {
		fmt.Println("12:00:00.000 E [f:1] не открылся WebSocket:", err)
		os.Exit(4)
	}
	fmt.Println("12:00:00.000 I [f:1] client expires in 88 days")
	fmt.Println("12:00:00.001 I [f:2] waiting for login — connect to \"openmediad\" from your Spotify app")
	time.Sleep(50 * time.Millisecond)
	fmt.Println("12:00:00.100 I [f:3] UCS login result: outcome=success attempts=1")

	c, err := net.Dial("unix", os.Getenv("OMD_PCM_SOCK"))
	if err != nil {
		fmt.Println("12:00:00.200 E [f:4] нет сокета движка:", err)
		os.Exit(5)
	}
	w := bufio.NewWriter(c)
	var corked atomic.Bool
	corked.Store(false)
	w.Write(hello(48000, 2))
	w.Write(cork(false))
	w.Flush()
	go func() { // команды приложения: pause → шим сообщает паузу
		for m := range srv.recv {
			if strings.Contains(m, `"pause"`) {
				corked.Store(true)
				c.Write(cork(true))
			}
		}
	}()
	go func() {
		srv.push(nopT{}, evAuth)
		srv.push(nopT{}, evPlaying)
	}()
	for {
		if !corked.Load() {
			if _, err := c.Write(frame(frData, pcm(480))); err != nil {
				io.Copy(io.Discard, os.Stdin)
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// nopT — минимальный testing.TB для вызова push вне теста.
type nopT struct{ testing.TB }

func (nopT) Helper()        {}
func (nopT) Fatal(a ...any) { fmt.Fprintln(os.Stderr, a...); os.Exit(6) }

type fakeMach struct {
	playing atomic.Bool
	pauses  atomic.Int32
	armed   atomic.Int32

	vmu       sync.Mutex
	virt      *mediad.Virtual
	virtCalls int
	vcmd      func(string)
}

func (m *fakeMach) Playing() bool { return m.playing.Load() }

// SetVirtual запоминает последнее состояние, которое компонент сообщил автомату.
func (m *fakeMach) SetVirtual(v *mediad.Virtual) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	if v == nil {
		m.virt = nil
	} else {
		cp := *v
		m.virt = &cp
	}
	m.virtCalls++
}

// ArmCarrier запоминает, сколько раз компонент просил отдать ему запуск Яндекса.
func (m *fakeMach) ArmCarrier(time.Duration) { m.armed.Add(1) }

func (m *fakeMach) OnVirtualCommand(f func(string)) { m.vmu.Lock(); m.vcmd = f; m.vmu.Unlock() }

func (m *fakeMach) virtual() (*mediad.Virtual, int) {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	return m.virt, m.virtCalls
}

func (m *fakeMach) platformCommand(cmd string) {
	m.vmu.Lock()
	f := m.vcmd
	m.vmu.Unlock()
	if f != nil {
		f(cmd)
	}
}
func (m *fakeMach) TakeOver() int {
	m.pauses.Add(1)
	m.playing.Store(false)
	return 1
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestComponentEndToEnd(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "omd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	run, _ := os.MkdirTemp("", "omr")
	defer os.RemoveAll(run)

	cfg := defaults()
	cfg.Dir, cfg.WSPort, cfg.FixCP15 = dir, freePort(t), false
	p := newPaths(dir, run, "")
	writeFile(t, p.soloist(), []byte("не нужен: загрузчик — копия тестового бинарника"))
	writeFile(t, p.loader(), selfBytes)
	writeFile(t, filepath.Join(p.lib(), "libc.so.6"), []byte("x"))
	writeFile(t, p.shim(), shimBlob)
	writeFile(t, p.key(), []byte("spak_testkey_1234567890\n"))
	t.Setenv("OMD_FAKE_SOLOIST", "1")

	master := newFakeMaster(volmap.Default().Master(10)) // колонка на уровне 10 из 20 = 50%
	prevOpen := openMaster
	openMaster = func() (masterVol, error) { return master, nil }
	defer func() { openMaster = prevOpen }()

	eng, o := newEngine()
	mach := &fakeMach{}
	mach.playing.Store(true) // играет Яндекс
	var logs atomic.Value
	logs.Store([]string{})
	logf := func(f string, a ...any) { logs.Store(append(logs.Load().([]string), fmt.Sprintf(f, a...))) }
	c := newComponent(*cfg, eng, mach, logf, run)

	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	waitFor(t, "звук Soloist дошёл до вывода движка", func() bool { return o.total() > 4800 })
	waitFor(t, "заиграл Spotify: музыка Яндекса на паузе", func() bool { return mach.pauses.Load() == 1 })
	waitFor(t, "вход виден в статусе", func() bool { return strings.Contains(c.StatusLine(), "в списке Spotify") })
	waitFor(t, "играет: исполнитель и трек", func() bool {
		return strings.Contains(c.StatusLine(), "играет: Группа, Гость — Песня")
	})
	if st := c.DebugState().(State); st.Proc.Pid == 0 || st.Player.Volume != 62 || !st.PCM.Playing || st.Expires == "" {
		t.Fatalf("состояние: %+v", st)
	}

	// громкость: Soloist показал 62%, а колонка на 50% — Spotify подстраивается под колонку
	waitFor(t, "Spotify приведён к громкости колонки", func() bool { v := c.DebugState().(State).Volume; return v != nil && v.ToSoloist >= 1 })
	if n := c.DebugState().(State).Volume.ToPlatform; n != 0 {
		t.Fatalf("платформу просить не должны, пока ползунок не двигали: запросов %d", n)
	}

	// заиграл Яндекс, пока играет Spotify: компонент ставит Spotify на паузу командой приложения
	mach.playing.Store(true)
	waitFor(t, "Spotify поставлен на паузу", func() bool { return !c.pcm.Playing() })
	if ys := c.StatusLine(); !strings.Contains(ys, "уступал звук: 2") {
		t.Fatalf("строка статуса: %s", ys)
	}

	c.Stop()
	if c.pcm.status().Streams != 0 {
		t.Fatal("после Stop остались потоки звука")
	}
	if _, err := os.Stat(p.pid()); err == nil {
		t.Fatal("pid-файл остался")
	}
	if runtime.GOOS == "linux" {
		if killOrphan(p.pid(), p.soloist()) {
			t.Fatal("после Stop остался чужой процесс")
		}
	}
}

func TestStartWithoutSetupOnlyReports(t *testing.T) {
	eng, _ := newEngine()
	var got string
	cfg := defaults()
	cfg.Dir = t.TempDir()
	c := newComponent(*cfg, eng, nil, func(f string, a ...any) { got = fmt.Sprintf(f, a...) }, t.TempDir())
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("без установки Start должен просто сообщить, а не падать: %v", err)
	}
	if !strings.Contains(got, "openmediad spotify setup") {
		t.Fatalf("в журнал не попала подсказка: %q", got)
	}
	if line := c.StatusLine(); !strings.Contains(line, "не настроен") || !strings.Contains(line, "ключ Soloist") {
		t.Fatalf("статус: %q", line)
	}
	c.Stop() // не запускали — не должно паниковать
	if st := c.DebugState().(State); len(st.NeedsSetup) == 0 {
		t.Fatal("в DebugState нет списка недостающего")
	}
}
