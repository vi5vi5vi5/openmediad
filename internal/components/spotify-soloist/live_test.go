//go:build linux

package spotifysoloist

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveShim — проверка настоящего шима (armhf) настоящим разбором кадров, на колонке. Запускается только вручную:
//
//	GOOS=linux GOARCH=arm64 go test -c -o /tmp/spotify.test ./internal/components/spotify-soloist/
//	# на колонке:
//	OMD_LIVE_LIB=/data/opt/openmediad/spotify-soloist/lib OMD_LIVE_SHIM_TEST=/путь/к/shim_test /tmp/spotify.test -test.run LiveShim -test.v
//
// OMD_LIVE_LIB — каталог с glibc 2.41 и libpulse.so.0 (после «openmediad spotify setup»), shim_test собирается shim/build.sh.
// Тест ничего не играет: звук принимает сам тест. Колонка и Soloist не затрагиваются.
func TestLiveShim(t *testing.T) {
	lib, shimTest := os.Getenv("OMD_LIVE_LIB"), os.Getenv("OMD_LIVE_SHIM_TEST")
	if lib == "" || shimTest == "" {
		t.Skip("OMD_LIVE_LIB и OMD_LIVE_SHIM_TEST не заданы (проверка на колонке)")
	}
	dir, err := os.MkdirTemp("", "omd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "pcm.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var (
		got         = map[byte]int{}
		dataBytes   int
		corks       []byte
		volumes     []int
		format      string
		maxLag      time.Duration
		connections int
		done        = make(chan struct{})
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections++
			mu.Unlock()
			go func() {
				r := bufio.NewReader(c)
				f, err := readHello(r)
				if err != nil {
					t.Errorf("привет: %v", err)
					return
				}
				mu.Lock()
				format = strings.Join([]string{itoa(f.Rate), itoa(f.Channels)}, "/")
				mu.Unlock()
				var buf []byte
				last := time.Now()
				for {
					typ, p, err := readFrame(r, &buf)
					if err != nil {
						close(done)
						return
					}
					mu.Lock()
					if lag := time.Since(last); lag > maxLag {
						maxLag = lag
					}
					last = time.Now()
					got[typ]++
					switch typ {
					case frData:
						dataBytes += len(p)
					case frCork:
						corks = append(corks, p[0])
					case frVolume:
						volumes = append(volumes, int(p[0])|int(p[1])<<8)
					}
					mu.Unlock()
				}
			}()
		}
	}()

	cmd := exec.Command(filepath.Join(lib, "ld-linux-armhf.so.3"), "--library-path", lib+":/lib", shimTest, "3", "0.15")
	cmd.Dir = lib
	cmd.Env = append(os.Environ(), "OMD_PCM_SOCK="+sock, "LD_LIBRARY_PATH="+lib)
	out, err := cmd.CombinedOutput()
	t.Logf("shim_test:\n%s", out)
	if err != nil {
		t.Fatalf("shim_test: %v", err)
	}
	if !strings.Contains(string(out), "ИТОГ ХОРОШО") {
		t.Fatal("shim_test не дал «ИТОГ ХОРОШО»")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("шим не закрыл сокет")
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("соединений %d, формат %s, кадров по типам %v, данных %d байт, пауз/продолжений %v, громкость %v, максимальная пауза между кадрами %s",
		connections, format, got, dataBytes, corks, volumes, maxLag)
	if connections < 1 || dataBytes < 100000 {
		t.Fatalf("звука от шима мало: %d байт", dataBytes)
	}
	if got[frFlush] < 1 {
		t.Error("flush не дошёл до сокета")
	}
	if len(corks) < 3 || corks[0] != 0 && corks[0] != 1 {
		t.Errorf("кадры паузы: %v", corks)
	}
	var sawPause, sawResume bool
	for _, c := range corks {
		sawPause = sawPause || c == 1
		sawResume = sawResume || c == 0
	}
	if !sawPause || !sawResume {
		t.Errorf("не видно и паузы, и продолжения: %v", corks)
	}
	if len(volumes) < 2 {
		t.Errorf("кадры громкости: %v", volumes)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
