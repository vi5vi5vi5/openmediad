package spotifysoloist

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock — часы стены и монотонные: тест двигает стену скачком (сеть выправила время), монотонные идут своим ходом.
type fakeClock struct {
	mu      sync.Mutex
	wall    time.Time
	elapsed time.Duration
}

func (f *fakeClock) src() (time.Time, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.elapsed += 5 * time.Millisecond // примерно столько проходит между проверками в тесте
	f.wall = f.wall.Add(5 * time.Millisecond)
	return f.wall, f.elapsed
}

func (f *fakeClock) jump(d time.Duration) { f.mu.Lock(); f.wall = f.wall.Add(d); f.mu.Unlock() }

func runWatch(t *testing.T, loggedIn bool) (*fakeClock, *atomic.Int32) {
	t.Helper()
	f := &fakeClock{wall: time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC)}
	var restarts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go watchClock(ctx, 5*time.Millisecond, 10*time.Millisecond, time.Minute, f.src,
		func() bool { return loggedIn }, func(time.Time) { restarts.Add(1) })
	return f, &restarts
}

func waitCount(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if n.Load() == want {
			return
		}
	}
	t.Fatalf("перезапусков %d, ждали %d", n.Load(), want)
}

// Колонка включилась с часами 1970 года, потом сеть выправила время, а Soloist так и не вошёл: его перезапускают, один раз.
func TestClockJumpWithoutLoginRestartsSoloist(t *testing.T) {
	f, restarts := runWatch(t, false)
	time.Sleep(30 * time.Millisecond)
	f.jump(56 * 365 * 24 * time.Hour)
	waitCount(t, restarts, 1)
	time.Sleep(100 * time.Millisecond)
	if restarts.Load() != 1 {
		t.Fatalf("перезапусков %d: один скачок — один перезапуск", restarts.Load())
	}
}

// Soloist уже вошёл (запустился после синхронизации или вошёл сам): трогать его незачем.
func TestClockJumpWhileLoggedInLeavesSoloistAlone(t *testing.T) {
	f, restarts := runWatch(t, true)
	time.Sleep(30 * time.Millisecond)
	f.jump(24 * time.Hour)
	time.Sleep(150 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatalf("перезапусков %d, а Soloist вошёл", restarts.Load())
	}
}

// Небольшая подстройка времени и откат назад выправлением не считаются.
func TestSmallDriftAndBackwardJumpAreIgnored(t *testing.T) {
	f, restarts := runWatch(t, false)
	time.Sleep(30 * time.Millisecond)
	f.jump(2 * time.Second)
	time.Sleep(50 * time.Millisecond)
	f.jump(-3 * time.Hour)
	time.Sleep(100 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatalf("перезапусков %d без выправления часов", restarts.Load())
	}
}
