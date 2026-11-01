package buttons

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRecognizesThePlatformLines(t *testing.T) {
	for line, want := range map[string]Action{
		"[1844:1883] [2026-11-01 03:35:36.830] [info] [buttons_actions.cpp:298] Toggle play/pause":     Toggle,
		"[1844:1892] [2026-11-01 03:35:33.090] [info] [buttons_actions.cpp:335] Request previous song": Prev,
		"[1844:1892] [2026-11-01 03:35:28.547] [info] [buttons_actions.cpp:342] Request next song":     Next,
	} {
		if got, ok := Parse([]byte(line)); !ok || got != want {
			t.Errorf("%q: %q %v, ждали %q", line, got, ok, want)
		}
	}
	for _, line := range []string{
		"[1844:1883] [2026-11-01 03:35:36.820] [buttons] [info] [swipe_manager.cpp:152] Button click: activate",
		"[1844:1902] [sdk] [info] [directive_sequencer.cpp:663] addDirectives [(xxe7716b59)playback_toggle_play_pause.PlaybackControlCapability]",
		"Toggle play/pause", // строка не из buttons_actions: не наша
		"",
	} {
		if a, ok := Parse([]byte(line)); ok {
			t.Errorf("%q принято за %q", line, a)
		}
	}
}

type recorder struct{ got []Action }

func (r *recorder) on(a Action) { r.got = append(r.got, a) }

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func line(msg string) string {
	return "[1] [2026-11-01 03:35:36.830] [info] [buttons_actions.cpp:298] " + msg + "\n"
}

func TestWatcherSkipsHistoryAndReportsNewActions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	appendFile(t, p, line("Toggle play/pause")) // старое нажатие
	r := &recorder{}
	w := &Watcher{Path: p, OnAction: r.on}
	w.Poll()
	if len(r.got) != 0 {
		t.Fatalf("прошлые нажатия повторены: %v", r.got)
	}
	appendFile(t, p, "[x] что-то другое\n"+line("Request next song")+line("Toggle play/pause"))
	w.Poll()
	if len(r.got) != 2 || r.got[0] != Next || r.got[1] != Toggle {
		t.Fatalf("получили %v", r.got)
	}
	w.Poll()
	if len(r.got) != 2 {
		t.Fatalf("повтор при пустом опросе: %v", r.got)
	}
}

func TestWatcherWaitsForTheEndOfAPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maind.log")
	appendFile(t, p, "старьё\n")
	r := &recorder{}
	w := &Watcher{Path: p, OnAction: r.on}
	w.Poll()
	full := line("Request previous song")
	appendFile(t, p, full[:30])
	w.Poll()
	if len(r.got) != 0 {
		t.Fatalf("недописанная строка принята: %v", r.got)
	}
	appendFile(t, p, full[30:])
	w.Poll()
	if len(r.got) != 1 || r.got[0] != Prev {
		t.Fatalf("получили %v", r.got)
	}
}

func TestWatcherFollowsLogRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "maind.log")
	appendFile(t, p, "длинная старая строка, чтобы файл был больше нового, ещё и ещё\n")
	r := &recorder{}
	w := &Watcher{Path: p, OnAction: r.on}
	w.Poll()
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, line("Toggle play/pause")) // новый файл короче старого
	w.Poll()
	if len(r.got) != 1 || r.got[0] != Toggle {
		t.Fatalf("после ротации получили %v", r.got)
	}
}

func TestWatcherWithoutLogIsQuiet(t *testing.T) {
	r := &recorder{}
	(&Watcher{Path: filepath.Join(t.TempDir(), "нет"), OnAction: r.on}).Poll()
	if len(r.got) != 0 {
		t.Fatal("нет файла — нет действий")
	}
}
