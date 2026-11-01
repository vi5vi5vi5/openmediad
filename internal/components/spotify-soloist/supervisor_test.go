package spotifysoloist

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeProc — подделка процесса: выходит, когда ему скажут (finish), по SIGTERM (exitOnTerm) или по Kill.
type fakeProc struct {
	pid        int
	done       chan error
	exitOnTerm bool
	once       sync.Once
	termed     atomic.Bool
	killed     atomic.Bool
}

func newFake(pid int, exitOnTerm bool) *fakeProc {
	return &fakeProc{pid: pid, done: make(chan error, 1), exitOnTerm: exitOnTerm}
}

func (f *fakeProc) finish(err error) { f.once.Do(func() { f.done <- err }) }
func (f *fakeProc) Pid() int         { return f.pid }
func (f *fakeProc) Wait() error      { return <-f.done }
func (f *fakeProc) Signal(s os.Signal) error {
	if s == syscall.SIGTERM {
		f.termed.Store(true)
		if f.exitOnTerm {
			f.finish(nil)
		}
	}
	return nil
}
func (f *fakeProc) Kill() error { f.killed.Store(true); f.finish(errors.New("убит")); return nil }

type exitErr int

func (e exitErr) Error() string { return "код выхода" }
func (e exitErr) ExitCode() int { return int(e) }

func fastSup(start func() (process, error)) *supervisor {
	return &supervisor{start: start, logf: func(string, ...any) {}, minWait: time.Millisecond, maxWait: 4 * time.Millisecond,
		probeInt: time.Millisecond, grace: 20 * time.Millisecond}
}

func runSup(s *supervisor) (cancel func()) {
	ctx, c := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	return func() { c(); <-done }
}

func TestRestartsAfterExit(t *testing.T) {
	var mu sync.Mutex
	var procs []*fakeProc
	s := fastSup(func() (process, error) {
		mu.Lock()
		defer mu.Unlock()
		p := newFake(100+len(procs), true)
		procs = append(procs, p)
		return p, nil
	})
	stop := runSup(s)
	waitFor(t, "первый запуск", func() bool { mu.Lock(); defer mu.Unlock(); return len(procs) == 1 })
	procs[0].finish(errors.New("упал"))
	waitFor(t, "перезапуск", func() bool { mu.Lock(); defer mu.Unlock(); return len(procs) == 2 })
	if st := s.status(); st.Restarts != 1 || st.Starts < 2 {
		t.Fatalf("статус %+v", st)
	}
	stop() // отмена: SIGTERM второму процессу, он выходит сам
	if !procs[1].termed.Load() || procs[1].killed.Load() {
		t.Fatal("при остановке процессу нужен SIGTERM, а не Kill")
	}
}

func TestExpiredBuildStopsRestarting(t *testing.T) {
	starts := 0
	var p *fakeProc
	s := fastSup(func() (process, error) {
		starts++
		p = newFake(1, true)
		p.finish(exitErr(10))
		return p, nil
	})
	done := make(chan struct{})
	go func() { s.run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("надзор не остановился после кода 10")
	}
	if st := s.status(); !st.Expired || starts != 1 {
		t.Fatalf("статус %+v, запусков %d", st, starts)
	}
}

func TestStartErrorsAreRetried(t *testing.T) {
	var n atomic.Int32
	s := fastSup(func() (process, error) {
		if n.Add(1) < 3 {
			return nil, errors.New("нет файла")
		}
		return newFake(7, true), nil
	})
	stop := runSup(s)
	defer stop()
	waitFor(t, "запуск после двух неудач", func() bool { return s.status().Pid == 7 })
}

func TestHangIsDetectedAndRestarted(t *testing.T) {
	var mu sync.Mutex
	var procs []*fakeProc
	s := fastSup(func() (process, error) {
		mu.Lock()
		defer mu.Unlock()
		p := newFake(200+len(procs), true)
		procs = append(procs, p)
		return p, nil
	})
	var clock atomic.Int64 // каждый замер «проходит» 2 с
	var ticks atomic.Uint64
	s.now = func() time.Time { return time.Unix(clock.Add(2), 0) }
	s.probe = func(pid int) (map[int]uint64, error) {
		if pid != 200 { // у перезапущенного процесса всё спокойно
			return map[int]uint64{1: 5}, nil
		}
		return map[int]uint64{1: ticks.Add(200)}, nil // 200 тиков за 2 с = 100% ядра
	}
	stop := runSup(s)
	defer stop()
	waitFor(t, "зависший процесс остановлен и запущен заново", func() bool { mu.Lock(); defer mu.Unlock(); return len(procs) >= 2 })
	if !procs[0].termed.Load() {
		t.Fatal("зависшему процессу не послан SIGTERM")
	}
	if st := s.status(); st.Hangs != 1 {
		t.Fatalf("зависаний %d, ждали 1", st.Hangs)
	}
}

func TestStubbornProcessIsKilled(t *testing.T) {
	p := newFake(1, false) // SIGTERM не слушает
	s := fastSup(func() (process, error) { return p, nil })
	stop := runSup(s)
	waitFor(t, "запущен", func() bool { return s.status().Pid == 1 })
	stop()
	if !p.killed.Load() {
		t.Fatal("после termGrace процесс должен быть убит")
	}
}

func TestHangDetector(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var d hangDetector
	busy := func(step int, ticks uint64) bool { // 2 с между замерами
		return d.observe(map[int]uint64{1: ticks, 2: 0}, t0.Add(time.Duration(step)*2*time.Second))
	}
	if busy(0, 0) || busy(1, 100) || busy(2, 200) { // 50% — не зависание
		t.Fatal("50% процессора приняты за зависание")
	}
	if busy(3, 400) || busy(4, 600) { // 100%, но только 2 замера подряд
		t.Fatal("зависание объявлено рано")
	}
	if !busy(5, 800) {
		t.Fatal("три замера подряд на 100% — зависание")
	}
	// всплеск сбрасывает счёт
	d = hangDetector{}
	busy(0, 0)
	busy(1, 200)
	busy(2, 400)
	busy(3, 410) // спокойно
	if busy(4, 610) {
		t.Fatal("счёт не сбросился после спокойного замера")
	}
}

func TestSoloistLogParsing(t *testing.T) {
	var logs []string
	keys := map[string]string{}
	l := &soloistLog{logf: func(f string, a ...any) { logs = append(logs, f) }, onKey: func(level, text string) { keys[level+text] = text }}
	l.Write([]byte("20:13:19.556 I [f:241   ] waiting for login — connect to \"x\"\n20:13:20.000 E [f:1] \x1b[1;31mошибка раз\x1b[0m\n"))
	l.Write([]byte("20:13:21.000 E [f:1] ошибка раз\n20:13:22.000 W [f:2] предупреждение\nстрока без формата\n20:13:23.000 I [f:3] обыч"))
	l.Write([]byte("ная\n"))
	if len(logs) != 2 { // ошибка раз (один раз, повтор подряд скрыт) и предупреждение
		t.Fatalf("в журнал ушло %d строк: %v", len(logs), logs)
	}
	if _, ok := keys["Iwaiting for login — connect to \"x\""]; !ok {
		t.Fatalf("ключевая строка не передана: %v", keys)
	}
	if got := l.lines(); len(got) != 6 || got[1] != "20:13:20.000 E [f:1] ошибка раз" {
		t.Fatalf("хвост журнала (ANSI должен быть вычищен): %q", got)
	}
}
