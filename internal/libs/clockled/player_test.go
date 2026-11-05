package clockled

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDisplay — дисплей, который «прошивка» может перерисовать в любой момент.
type fakeDisplay struct {
	mu      sync.Mutex
	cur     Frame
	writes  []Frame
	readErr error
}

func (d *fakeDisplay) Read() (Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur, d.readErr
}
func (d *fakeDisplay) Write(f Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cur = f
	d.writes = append(d.writes, f)
	return nil
}
func (d *fakeDisplay) frame() Frame { d.mu.Lock(); defer d.mu.Unlock(); return d.cur }
func (d *fakeDisplay) firmware(f Frame) {
	d.mu.Lock()
	d.cur = f
	d.mu.Unlock()
}
func (d *fakeDisplay) nwrites() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.writes) }

// timeShown — «12:34» на дисплее до анимации.
var timeShown = seg(map[string]uint8{"B0": 0xff, "C0": 0xff, "A1": 0xff, "DH": 0xff, "DL": 0xff})

func quick(frames ...map[string]uint8) *Anim {
	var steps []Step
	for _, f := range frames {
		steps = append(steps, Step{seg(f), ms(30)})
	}
	a, err := ParseAnim(stream(false, steps...))
	if err != nil {
		panic(err)
	}
	return a
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

func newPlayer(d *fakeDisplay) (*Player, *[]string) {
	var mu sync.Mutex
	var logs []string
	return &Player{Dev: d, Tick: 5 * time.Millisecond, Logf: func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, f)
		mu.Unlock()
	}}, &logs
}

// Анимация проигрывается кадр за кадром, а потом возвращается то, что было на дисплее (время): сама прошивка вернула бы его только через минуту.
func TestPlayerShowsTheAnimationAndRestoresTheTime(t *testing.T) {
	d := &fakeDisplay{cur: timeShown}
	p, _ := newPlayer(d)
	defer p.Close()
	p.Play(quick(map[string]uint8{"B1": 0xff, "E2": 0xff}, map[string]uint8{"B1": 0x80}), 255, "пауза")
	waitFor(t, "первый кадр", func() bool { return d.frame() == seg(map[string]uint8{"B1": 0xff, "E2": 0xff}) })
	waitFor(t, "второй кадр", func() bool { return d.frame() == seg(map[string]uint8{"B1": 0x80}) })
	waitFor(t, "время вернулось", func() bool { return d.frame() == timeShown && !p.Active() })
}

func TestPlayerScalesBrightness(t *testing.T) {
	d := &fakeDisplay{}
	p, _ := newPlayer(d)
	defer p.Close()
	p.Play(quick(map[string]uint8{"B1": 200}), 128, "пауза")
	waitFor(t, "приглушённый кадр", func() bool { f := d.frame(); i, _ := Index("B1"); return f[i] >= 99 && f[i] <= 101 })
}

// Прошивка нарисовала своё посреди анимации (сменилась минута, началась анимация Алисы): уступаем и чужое не затираем.
func TestPlayerYieldsWhenTheFirmwareDrawsOverIt(t *testing.T) {
	d := &fakeDisplay{cur: timeShown}
	p, logs := newPlayer(d)
	defer p.Close()
	a := quick(map[string]uint8{"B1": 0xff}, map[string]uint8{"B1": 0x80}, map[string]uint8{"B1": 0x40}, map[string]uint8{"B1": 0x20})
	p.Play(a, 255, "дальше")
	waitFor(t, "первый кадр", func() bool { return d.frame() == seg(map[string]uint8{"B1": 0xff}) })
	newTime := seg(map[string]uint8{"B0": 0xff, "A1": 0xff, "DH": 0xff, "DL": 0xff, "D1": 0xff})
	d.firmware(newTime)
	waitFor(t, "анимация остановилась", func() bool { return !p.Active() })
	time.Sleep(50 * time.Millisecond)
	if d.frame() != newTime {
		t.Fatalf("затёрли то, что нарисовала прошивка: %v", d.frame().Lit())
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "уступаю") {
		t.Fatal("в журнале нет причины")
	}
}

// Новая анимация заменяет играющую; вернуть надо то, что было до первой, а не кадр первой анимации.
func TestPlayerReplacesTheRunningAnimationAndKeepsTheOriginalBase(t *testing.T) {
	d := &fakeDisplay{cur: timeShown}
	p, _ := newPlayer(d)
	defer p.Close()
	long, _ := ParseAnim(stream(false, Step{seg(map[string]uint8{"A3": 0x11}), ms(2000)}))
	p.Play(long, 255, "первая")
	waitFor(t, "первая играет", func() bool { return d.frame() == seg(map[string]uint8{"A3": 0x11}) })
	p.Play(quick(map[string]uint8{"G0": 0x22}), 255, "вторая")
	waitFor(t, "вторая играет", func() bool { return d.frame() == seg(map[string]uint8{"G0": 0x22}) })
	waitFor(t, "вернулось время, а не кадр первой", func() bool { return d.frame() == timeShown && !p.Active() })
}

func TestPlayerCloseRestoresAndStops(t *testing.T) {
	d := &fakeDisplay{cur: timeShown}
	p, _ := newPlayer(d)
	long, _ := ParseAnim(stream(false, Step{seg(map[string]uint8{"A3": 0x11}), ms(5000)}))
	p.Play(long, 255, "долгая")
	waitFor(t, "играет", func() bool { return d.frame() == seg(map[string]uint8{"A3": 0x11}) })
	p.Close()
	if d.frame() != timeShown {
		t.Fatalf("при остановке не вернули время: %v", d.frame().Lit())
	}
	n := d.nwrites()
	p.Play(quick(map[string]uint8{"B1": 1}), 255, "после Close")
	time.Sleep(60 * time.Millisecond)
	if d.nwrites() != n {
		t.Fatal("после Close анимации ещё играют")
	}
}

func TestPlayerStopsOnReadErrorAndEmptyAnimationIsIgnored(t *testing.T) {
	d := &fakeDisplay{readErr: errors.New("нет sysfs")}
	p, logs := newPlayer(d)
	defer p.Close()
	p.Play(quick(map[string]uint8{"B1": 1}), 255, "пауза")
	waitFor(t, "остановился", func() bool { return !p.Active() })
	if d.nwrites() != 0 || !strings.Contains(strings.Join(*logs, "\n"), "чтение") {
		t.Fatalf("записей %d, журнал %v", d.nwrites(), *logs)
	}
	p.Play(nil, 255, "пусто")
	p.Play(&Anim{}, 255, "пусто")
	if p.Active() {
		t.Fatal("пустая анимация запустила проигрыватель")
	}
}
