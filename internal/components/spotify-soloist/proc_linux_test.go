//go:build linux

package spotifysoloist

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestThreadTicksReadsOwnProcess(t *testing.T) {
	a, err := threadTicks(os.Getpid())
	if err != nil || len(a) == 0 {
		t.Fatalf("threadTicks(self) = %v, %v", a, err)
	}
	// горячий цикл на полсекунды: хоть у одного потока ощутимо выросли тики
	end := time.Now().Add(500 * time.Millisecond)
	for x := 0; time.Now().Before(end); x++ {
	}
	b, err := threadTicks(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var grew bool
	for tid, v := range b {
		if v > a[tid]+10 {
			grew = true
		}
	}
	if !grew {
		t.Fatalf("тики потоков не выросли за 0,5 с счёта: %v → %v", a, b)
	}
	if _, err := threadTicks(1 << 30); err == nil {
		t.Fatal("несуществующий процесс не вернул ошибку")
	}
}

func TestKillOrphan(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "soloist")
	// «Soloist»: любой процесс, в командной строке которого есть путь к нему
	cmd := exec.Command("sh", "-c", "sleep 30", fake) // $0 = путь: он попадёт в командную строку
	if cmd.Err != nil {
		t.Skip("нет sh")
	}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	go cmd.Wait()
	defer cmd.Process.Kill()
	pid := filepath.Join(dir, "pid")
	os.WriteFile(pid, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)

	if killOrphan(filepath.Join(dir, "нет-файла"), fake) {
		t.Fatal("убит процесс без pid-файла")
	}
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	if killOrphan(other, filepath.Join(dir, "чужая-программа")) {
		t.Fatal("убит процесс, в командной строке которого нет нашего Soloist")
	}
	if !killOrphan(pid, fake) {
		t.Fatal("оставшийся Soloist не убит")
	}
	if _, err := os.Stat(pid); err == nil {
		t.Fatal("pid-файл не удалён")
	}
	waitFor(t, "процесс завершился", func() bool { return exec.Command("kill", "-0", strconv.Itoa(cmd.Process.Pid)).Run() != nil })
}
