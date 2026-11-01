package led

import (
	"sync"
	"testing"
	"time"
)

func TestParseColor(t *testing.T) {
	for in, want := range map[string]Color{"#ff8000": {255, 128, 0}, "00ff40": {0, 255, 64}, " #0060FF ": {0, 96, 255}} {
		if got, err := ParseColor(in); err != nil || got != want {
			t.Errorf("ParseColor(%q) = %v, %v; ждали %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "#fff", "#ggggggg", "красный", "#12345"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
	if (Color{255, 128, 0}).Hex() != "#ff8000" {
		t.Error("Hex")
	}
}

func TestScale(t *testing.T) {
	c := Color{200, 100, 50}
	if got := c.Scale(255); got != c {
		t.Errorf("Scale(255) = %v", got)
	}
	if got := c.Scale(0); !got.IsZero() {
		t.Errorf("Scale(0) = %v", got)
	}
	if got := c.Scale(128); got != (Color{100, 50, 25}) {
		t.Errorf("Scale(128) = %v", got)
	}
}

func TestPanelShowsMostRecentActive(t *testing.T) {
	p := NewPanel()
	now := time.Unix(100, 0)
	p.now = func() time.Time { now = now.Add(time.Second); return now }
	if s := p.State(); s.Source != nil || s.Output != nil {
		t.Fatalf("пустая панель: %+v", s)
	}
	p.On(Source, "yandex-music", Color{255, 200, 0})
	p.On(Source, "spotify-soloist", Color{0, 255, 64})
	if s := p.State(); s.Source == nil || s.Source.Name != "spotify-soloist" {
		t.Fatalf("побеждает последний включённый: %+v", s.Source)
	}
	p.Off(Source, "spotify-soloist")
	if s := p.State(); s.Source == nil || s.Source.Name != "yandex-music" {
		t.Fatalf("после выключения последнего возвращается предыдущий: %+v", s.Source)
	}
	p.Off(Source, "yandex-music")
	p.Off(Source, "yandex-music") // повторное выключение безвредно
	if s := p.State(); s.Source != nil {
		t.Fatalf("всё выключено: %+v", s.Source)
	}
	p.On(Output, "bt-out", Color{0, 96, 255})
	if s := p.State(); s.Output == nil || s.Output.Name != "bt-out" || s.Source != nil {
		t.Fatalf("роли независимы: %+v", s)
	}
}

func TestRepeatedOnDoesNotReorderOrNotify(t *testing.T) {
	p := NewPanel()
	var mu sync.Mutex
	var calls int
	p.Watch(func(State) { mu.Lock(); calls++; mu.Unlock() }) // первый вызов — сразу, с пустым состоянием
	p.On(Source, "a", Color{1, 2, 3})
	p.On(Source, "b", Color{4, 5, 6})
	mu.Lock()
	n := calls
	mu.Unlock()
	p.On(Source, "a", Color{1, 2, 3}) // уже включён: ничего не меняется, «b» остаётся последним
	if s := p.State(); s.Source.Name != "b" {
		t.Fatalf("повторное включение переставило порядок: %+v", s.Source)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != n {
		t.Fatalf("наблюдателя разбудили зря: %d → %d", n, calls)
	}
}

func TestWatchGetsCurrentStateThenChanges(t *testing.T) {
	p := NewPanel()
	p.On(Source, "a", Color{1, 1, 1})
	var got []string
	stop := p.Watch(func(s State) {
		if s.Source != nil {
			got = append(got, s.Source.Name)
		} else {
			got = append(got, "-")
		}
	})
	p.On(Source, "b", Color{})
	p.Off(Source, "b")
	p.Off(Source, "a")
	stop()
	p.On(Source, "c", Color{}) // после stop наблюдатель молчит
	want := []string{"a", "b", "a", "-"}
	if len(got) != len(want) {
		t.Fatalf("наблюдатель получил %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("наблюдатель получил %v, ждали %v", got, want)
		}
	}
}

func TestChangedDefaultColorWakesWatchers(t *testing.T) {
	p := NewPanel()
	var last Color
	p.Watch(func(s State) {
		if s.Source != nil {
			last = s.Source.Default
		}
	})
	p.On(Source, "a", Color{1, 1, 1})
	p.On(Source, "a", Color{9, 9, 9})
	if last != (Color{9, 9, 9}) {
		t.Fatalf("новый предложенный цвет не дошёл: %v", last)
	}
}

func TestBadArgumentsAreIgnored(t *testing.T) {
	p := NewPanel()
	p.On(Source, "", Color{})
	p.On(Role(7), "x", Color{})
	p.Off(Role(-1), "x")
	if s := p.State(); s.Source != nil || s.Output != nil {
		t.Fatalf("мусор попал в панель: %+v", s)
	}
}

func TestWatcherCanCallPanelWithoutDeadlock(t *testing.T) {
	p := NewPanel()
	done := make(chan struct{})
	go func() {
		p.Watch(func(State) { p.State() }) // наблюдатель вызывается без замка
		p.On(Source, "a", Color{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("взаимная блокировка")
	}
}
