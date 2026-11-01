package spotifysoloist

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openmediad/internal/libs/volmap"
)

func TestSupervisorRestartOnRequest(t *testing.T) {
	var procs []*fakeProc
	s := fastSup(func() (process, error) {
		p := newFake(300+len(procs), true)
		procs = append(procs, p)
		return p, nil
	})
	s.kick = make(chan struct{}, 1)
	stop := runSup(s)
	defer stop()
	waitFor(t, "запуск", func() bool { return s.status().Starts == 1 })
	s.restart()
	waitFor(t, "перезапуск по просьбе", func() bool { return s.status().Starts == 2 })
	if st := s.status(); st.Restarts != 0 || st.Hangs != 0 {
		t.Fatalf("перезапуск по просьбе не сбой: %+v", st)
	}
	if !procs[0].termed.Load() {
		t.Fatal("старому процессу не послан SIGTERM")
	}
}

func TestReloadRenamesWithoutDaemonRestart(t *testing.T) {
	self, _ := os.Executable()
	selfBytes, _ := os.ReadFile(self)
	dir, _ := os.MkdirTemp("", "omd")
	defer os.RemoveAll(dir)
	run, _ := os.MkdirTemp("", "omr")
	defer os.RemoveAll(run)
	cfg := defaults()
	cfg.Dir, cfg.WSPort, cfg.FixCP15 = dir, freePort(t), false
	p := newPaths(dir, run, "")
	writeFile(t, p.soloist(), []byte("x"))
	writeFile(t, p.loader(), selfBytes)
	writeFile(t, filepath.Join(p.lib(), "libc.so.6"), []byte("x"))
	writeFile(t, p.shim(), shimBlob)
	writeFile(t, p.key(), []byte("spak_testkey_1234567890\n"))
	t.Setenv("OMD_FAKE_SOLOIST", "1")
	master := newFakeMaster(volmap.Default().Master(10))
	prev := openMaster
	openMaster = func() (masterVol, error) { return master, nil }
	defer func() { openMaster = prev }()

	eng, _ := newEngine()
	var logs []string
	c := newComponent(*cfg, eng, nil, func(f string, a ...any) { logs = append(logs, f) }, run)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	waitFor(t, "Soloist запущен", func() bool { return c.sup.status().Pid != 0 })

	renamed := *cfg
	renamed.DeviceName = "Кухня"
	if got := c.Reload(&renamed); len(got) != 0 {
		t.Fatalf("переименование не требует перезапуска демона, а вернулось %v", got)
	}
	if c.config().DeviceName != "Кухня" {
		t.Fatal("имя не применилось")
	}
	waitFor(t, "Soloist перезапущен с новым именем", func() bool { return c.sup.status().Starts == 2 })

	// ключи, которым нужен перезапуск демона, перечислены; Soloist из-за них не перезапускается
	moved := renamed
	moved.WSPort, moved.VolumeSync, moved.FixCP15 = cfg.WSPort+1, false, true
	got := c.Reload(&moved)
	want := map[string]bool{"ws_port": true, "volume_sync": true, "fix_cp15": true}
	if len(got) != 3 {
		t.Fatalf("требуют перезапуска демона: %v", got)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("лишний ключ %q", k)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if c.sup.status().Starts != 2 {
		t.Fatalf("Soloist перезапустили из-за ключей, которым нужен перезапуск демона: запусков %d", c.sup.status().Starts)
	}

	// неверные настройки отвергаются целиком
	bad := renamed
	bad.CacheMB = 5
	c.Reload(&bad)
	if c.config().CacheMB != cfg.CacheMB {
		t.Fatal("неверные настройки применились")
	}
}
