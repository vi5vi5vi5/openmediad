package stock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/platform/proc"
)

// tcpLine — строка /proc/net/tcp.
func tcpLine(local, remote, st string) string {
	return fmt.Sprintf("   0: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 20 4 30 10 -1", local, remote, st)
}

func writeTCP(t *testing.T, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Join(proc.Root, "net"), 0o755)
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	os.WriteFile(filepath.Join(proc.Root, "net", "tcp"), []byte(head+strings.Join(lines, "\n")+"\n"), 0o644)
}

func fakeRoot(t *testing.T) {
	old := proc.Root
	proc.Root = t.TempDir()
	t.Cleanup(func() { proc.Root = old })
}

// Соединения как на колонке: 4 клиента платформы через шим, 4 соединения шима к mediad с 127.0.0.2, слушатели.
func TestReadConnsViaShim(t *testing.T) {
	fakeRoot(t)
	var ls []string
	ls = append(ls, tcpLine("0100007F:26B2", "00000000:0000", "0A"), tcpLine("0100007F:4DC2", "00000000:0000", "0A"))
	for i := 0; i < 4; i++ {
		port := fmt.Sprintf("%04X", 40000+i)
		ls = append(ls,
			tcpLine("0100007F:"+port, "0100007F:26B2", "01"),                         // клиент platformd (думает, что говорит с 9906)
			tcpLine("0100007F:4DC2", "0100007F:"+port, "01"),                         // та же связь со стороны шима
			tcpLine("0200007F:"+fmt.Sprintf("%04X", 50000+i), "0100007F:26B2", "01"), // шим → mediad
			tcpLine("0100007F:26B2", "0200007F:"+fmt.Sprintf("%04X", 50000+i), "01"), // со стороны mediad
		)
	}
	ls = append(ls, tcpLine("0100007F:9999", "0100007F:26B2", "06")) // TIME_WAIT не считается
	writeTCP(t, ls...)
	c, err := ReadConns()
	if err != nil || c.ViaShim != 4 || c.Direct != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestReadConnsDirect(t *testing.T) {
	fakeRoot(t)
	var ls []string
	for i := 0; i < 4; i++ {
		port := fmt.Sprintf("%04X", 40000+i)
		ls = append(ls, tcpLine("0100007F:"+port, "0100007F:26B2", "01"), tcpLine("0100007F:26B2", "0100007F:"+port, "01"))
	}
	writeTCP(t, ls...)
	if c, _ := ReadConns(); c.ViaShim != 0 || c.Direct != 4 {
		t.Fatalf("%+v", c)
	}
}

func TestRerouterRestartsOnceWhenDirect(t *testing.T) {
	fakeRoot(t)
	direct := func(n int) {
		var ls []string
		for i := 0; i < 4; i++ {
			port := fmt.Sprintf("%04X", 40000+i)
			ls = append(ls, tcpLine("0100007F:"+port, "0100007F:26B2", "01"))
			if i >= n {
				ls = append(ls, tcpLine("0100007F:4DC2", "0100007F:"+port, "01"))
			}
		}
		writeTCP(t, ls...)
	}
	direct(4)
	var restarts atomic.Int32
	r := &Rerouter{
		Restart:  func(context.Context) error { restarts.Add(1); direct(0); return nil },
		DaemonUp: func() bool { return true },
		Settle:   50 * time.Millisecond, Poll: 10 * time.Millisecond, Timeout: 2 * time.Second,
	}
	c, err := r.Run(context.Background())
	if err != nil || c.ViaShim != 4 || restarts.Load() != 1 {
		t.Fatalf("%+v %v, перезапусков %d", c, err, restarts.Load())
	}
	// уже через шим — ничего не перезапускается
	c, err = r.Run(context.Background())
	if err != nil || restarts.Load() != 1 {
		t.Fatalf("повторный Run: %+v %v, перезапусков %d", c, err, restarts.Load())
	}
}

func TestRerouterStopsWhenDaemonDies(t *testing.T) {
	fakeRoot(t)
	writeTCP(t)
	r := &Rerouter{DaemonUp: func() bool { return false }, Poll: 10 * time.Millisecond, Timeout: time.Second}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "демон") {
		t.Fatalf("ждали ошибку «демон не работает», получили %v", err)
	}
}
