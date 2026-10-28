package journal

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRingKeepsNewestInOrder(t *testing.T) {
	j := &Journal{Cap: 5}
	for i := 1; i <= 12; i++ {
		j.Log(Info, "t", "m", "", map[string]any{"i": i})
	}
	es := j.Since(0, 0)
	if len(es) != 5 {
		t.Fatalf("в кольце %d записей, ждали 5", len(es))
	}
	for i, e := range es {
		if want := uint64(8 + i); e.ID != want {
			t.Fatalf("запись %d имеет ID %d, ждали %d: порядок или состав нарушен", i, e.ID, want)
		}
	}
	if got := j.Since(10, 0); len(got) != 2 || got[0].ID != 11 {
		t.Fatalf("Since(10) вернул %v", got)
	}
	if got := j.Since(0, 2); len(got) != 2 || got[1].ID != 12 {
		t.Fatalf("limit должен оставлять самые свежие: %v", got)
	}
}

func TestSlowSubscriberDoesNotBlockLogging(t *testing.T) {
	j := &Journal{}
	ch, unsub := j.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ { // подписчик не читает, канал на 256
			j.Log(Info, "t", "m", "", nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("журнал завис на медленном подписчике")
	}
	if len(ch) != 256 {
		t.Fatalf("в канале %d записей, ждали 256 (остальные пропущены)", len(ch))
	}
}

func TestLevelFilterAndOut(t *testing.T) {
	var out bytes.Buffer
	j := &Journal{Out: &out}
	j.Log(Trace, "player", "прогресс", "", nil)
	j.Log(Info, "player", "играет", "", map[string]any{"pos_ms": 1000})
	if es := j.Since(0, 0); len(es) != 1 || es[0].Msg != "играет" {
		t.Fatalf("по умолчанию trace не сохраняется: %v", es)
	}
	if !strings.Contains(out.String(), "info  player      играет pos_ms=1000") {
		t.Fatalf("строка в Out: %q", out.String())
	}
	j.SetLevel(Trace)
	j.Log(Trace, "player", "прогресс", "", nil)
	if es := j.Since(0, 0); len(es) != 2 || j.Level() != Trace {
		t.Fatalf("после SetLevel(trace) прогресс сохраняется: %v", es)
	}
	j.SetLevel(Warn)
	j.Log(Info, "x", "мелочь", "", nil)
	if es := j.Since(0, 0); len(es) != 2 {
		t.Fatalf("при warn info не сохраняется: %v", es)
	}
}

func TestFileWriterRotates(t *testing.T) {
	dir := t.TempDir()
	w := &FileWriter{Path: dir + "/log/openmediad.log", Max: 100}
	defer w.Close()
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(strings.Repeat("x", 29) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(w.Path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) > 100 || len(old) > 100 || len(cur)+len(old) < 100 {
		t.Fatalf("размеры: текущий %d, прежний %d (предел 100)", len(cur), len(old))
	}
}
