package spotifysoloist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// exitExpired — код выхода Soloist: сборка истекла (90 дней). Перезапуск бесполезен.
const exitExpired = 10

// Надзор: запуск Soloist, перезапуск с нарастающей паузой, обнаружение зависания, мягкая остановка.
const (
	backoffMin   = 2 * time.Second
	backoffMax   = 60 * time.Second
	stableAfter  = 2 * time.Minute // дольше проработал — паузу перед следующим перезапуском сбрасываем
	probeEvery   = 2 * time.Second
	termGrace    = 5 * time.Second
	hangBusy     = 0.9 // доля процессорного времени одного потока, с которой он считается зависшим
	hangSamples  = 3   // столько замеров подряд
	clockTicksPS = 100 // USER_HZ ядра Linux
)

// process — запущенный Soloist (подменяется в тестах).
type process interface {
	Pid() int
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type execProc struct{ cmd *exec.Cmd }

func (p execProc) Pid() int                 { return p.cmd.Process.Pid }
func (p execProc) Wait() error              { return p.cmd.Wait() }
func (p execProc) Signal(s os.Signal) error { return p.cmd.Process.Signal(s) }
func (p execProc) Kill() error              { return p.cmd.Process.Kill() }

// supervisor запускает и сторожит Soloist.
type supervisor struct {
	start func() (process, error)               // запуск (в тестах — подделка)
	probe func(pid int) (map[int]uint64, error) // тики процессора по потокам; nil — без обнаружения зависания
	logf  func(string, ...any)
	now   func() time.Time

	// Времена; ноль — значения по умолчанию (в тестах короткие).
	minWait, maxWait, probeInt, grace time.Duration

	kick chan struct{} // просьба перезапустить Soloist сейчас (новые настройки), не считая это сбоем

	mu       sync.Mutex
	state    string // что с процессом сейчас, одной строкой
	pid      int
	starts   int
	restarts int // сколько раз перезапускали из-за выхода или зависания
	hangs    int
	expired  bool
	lastExit string
}

// restart просит перезапустить Soloist (не блокируется; повторные просьбы сливаются).
func (s *supervisor) restart() {
	s.mu.Lock()
	k := s.kick
	s.mu.Unlock()
	if k == nil {
		return
	}
	select {
	case k <- struct{}{}:
	default:
	}
}

func (s *supervisor) setState(f string, a ...any) {
	s.mu.Lock()
	s.state = fmt.Sprintf(f, a...)
	s.mu.Unlock()
}

type supStatus struct {
	State    string `json:"state"`
	Pid      int    `json:"pid,omitempty"`
	Starts   int    `json:"starts"`
	Restarts int    `json:"restarts"`
	Hangs    int    `json:"hangs"`
	Expired  bool   `json:"expired,omitempty"`
	LastExit string `json:"last_exit,omitempty"`
}

func (s *supervisor) status() supStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return supStatus{State: s.state, Pid: s.pid, Starts: s.starts, Restarts: s.restarts, Hangs: s.hangs, Expired: s.expired, LastExit: s.lastExit}
}

// run ведёт Soloist до отмены ctx; перед возвратом Soloist остановлен.
func (s *supervisor) run(ctx context.Context) {
	lockThread() // Pdeathsig привязан к потоку ОС: пусть это будет поток, который живёт, пока работает надзор
	if s.now == nil {
		s.now = time.Now
	}
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	if s.minWait == 0 {
		s.minWait = backoffMin
	}
	if s.maxWait == 0 {
		s.maxWait = backoffMax
	}
	if s.probeInt == 0 {
		s.probeInt = probeEvery
	}
	if s.grace == 0 {
		s.grace = termGrace
	}
	wait := s.minWait
	for ctx.Err() == nil {
		p, err := s.start()
		if err != nil {
			s.setState("не запускается: %v", err)
			s.logf("Soloist не запустился: %v; повтор через %s", err, wait)
			if !sleep(ctx, wait) {
				return
			}
			wait = s.next(wait)
			continue
		}
		began := s.now()
		s.mu.Lock()
		s.pid = p.Pid()
		s.starts++
		s.mu.Unlock()
		s.setState("работает (pid %d)", p.Pid())
		exited := make(chan error, 1)
		go func() { exited <- p.Wait() }()

		err, hung, kicked := s.watch(ctx, p, exited)
		s.mu.Lock()
		s.pid = 0
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		code := exitCode(err)
		switch {
		case kicked:
			s.logf("Soloist перезапущен: применены новые настройки")
			s.setState("перезапуск (новые настройки)")
			if !sleep(ctx, 500*time.Millisecond) { // порты и сокет успевают освободиться
				return
			}
			continue
		case hung:
			s.mu.Lock()
			s.hangs++
			s.lastExit = "завис: поток держал процессор на 100%"
			s.mu.Unlock()
			s.logf("Soloist завис (один поток на 100%% процессора) и перезапущен; если это повторяется, проверьте abi.cp15_barrier")
		case code == exitExpired:
			s.mu.Lock()
			s.expired = true
			s.lastExit = "сборка истекла (код 10)"
			s.mu.Unlock()
			s.setState("сборка Soloist истекла: openmediad spotify update")
			s.logf("сборка Soloist истекла (код выхода 10): перезапуск не поможет, нужно обновить — openmediad spotify update")
			return
		default:
			s.mu.Lock()
			s.lastExit = fmt.Sprintf("вышел: %v", err)
			s.mu.Unlock()
			s.logf("Soloist вышел (%v); перезапуск через %s", err, wait)
		}
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		if s.now().Sub(began) > stableAfter {
			wait = s.minWait
		}
		s.setState("перезапуск через %s", wait)
		if !sleep(ctx, wait) {
			return
		}
		wait = s.next(wait)
	}
}

// watch ждёт выхода процесса, зависания или отмены. hung — процесс остановлен из-за зависания.
func (s *supervisor) watch(ctx context.Context, p process, exited chan error) (err error, hung, kicked bool) {
	var tick <-chan time.Time
	det := hangDetector{}
	if s.probe != nil {
		t := time.NewTicker(s.probeInt)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return s.terminate(p, exited), false, false
		case err := <-exited:
			return err, false, false
		case <-s.kick:
			return s.terminate(p, exited), false, true
		case <-tick:
			ticks, perr := s.probe(p.Pid())
			if perr == nil && det.observe(ticks, s.now()) {
				return s.terminate(p, exited), true, false
			}
		}
	}
}

// terminate просит процесс завершиться (SIGTERM) и через s.grace добивает. Если сигнал послать нельзя (процесса уже нет или
// система не умеет), сразу Kill.
func (s *supervisor) terminate(p process, exited chan error) error {
	if err := p.Signal(syscall.SIGTERM); err != nil {
		select {
		case err := <-exited:
			return err
		default:
		}
		p.Kill()
		return <-exited
	}
	select {
	case err := <-exited:
		return err
	case <-time.After(s.grace):
	}
	p.Kill()
	return <-exited
}

func (s *supervisor) next(w time.Duration) time.Duration {
	if w *= 2; w > s.maxWait {
		w = s.maxWait
	}
	return w
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func exitCode(err error) int {
	var ee interface{ ExitCode() int } // *exec.ExitError и подделки в тестах
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// hangDetector считает зависшим процесс, у которого какой-то поток подряд hangSamples замеров занимает не меньше hangBusy
// одного ядра. Soloist при воспроизведении тоже грузит процессор, но декодирование одного потока на колонке — десятки процентов
// и не держится на 90% подряд несколько замеров.
type hangDetector struct {
	prev map[int]uint64
	at   time.Time
	bad  int
}

func (d *hangDetector) observe(ticks map[int]uint64, now time.Time) bool {
	prev, at := d.prev, d.at
	d.prev, d.at = ticks, now
	if prev == nil || !now.After(at) {
		return false
	}
	span := now.Sub(at).Seconds()
	busy := 0.0
	for tid, t := range ticks {
		if p, ok := prev[tid]; ok && t >= p {
			if b := float64(t-p) / (clockTicksPS * span); b > busy {
				busy = b
			}
		}
	}
	if busy >= hangBusy {
		d.bad++
	} else {
		d.bad = 0
	}
	return d.bad >= hangSamples
}

// ---------- строки журнала Soloist ----------

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// logLine — строка вывода Soloist: «20:13:19.556 I [f:241    ] текст».
var logLineRE = regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d+ ([A-Z]) \[[^\]]*\] (.*)$`)

// soloistLog разбирает вывод Soloist: хвост для отладки, предупреждения и ошибки в журнал, ключевые события в состояние.
type soloistLog struct {
	logf  func(string, ...any)
	onKey func(key, text string)

	mu   sync.Mutex
	buf  []byte
	tail []string
	last string
	n    int
}

const tailLines = 60

func (l *soloistLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf = append(l.buf, p...)
	var lines []string
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 8192 { // строка без конца: не копить
		l.buf = l.buf[:0]
	}
	l.mu.Unlock()
	for _, ln := range lines {
		l.line(strings.TrimRight(ansiRE.ReplaceAllString(ln, ""), "\r"))
	}
	return len(p), nil
}

func (l *soloistLog) line(s string) {
	if s == "" {
		return
	}
	l.mu.Lock()
	l.tail = append(l.tail, s)
	if len(l.tail) > tailLines {
		l.tail = l.tail[len(l.tail)-tailLines:]
	}
	l.mu.Unlock()
	m := logLineRE.FindStringSubmatch(s)
	if m == nil {
		return
	}
	level, text := m[1], m[2]
	if l.onKey != nil {
		l.onKey(level, text)
	}
	if level != "E" && level != "W" {
		return
	}
	l.mu.Lock()
	dup := text == l.last
	l.last = text
	l.n++
	l.mu.Unlock()
	if !dup {
		l.logf("Soloist (%s): %s", level, text)
	}
}

func (l *soloistLog) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.tail...)
}
