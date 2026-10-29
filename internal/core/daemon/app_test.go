package daemon

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/wire"

	_ "openmediad/internal/components/alice-voice"
	_ "openmediad/internal/components/catalog"
	_ "openmediad/internal/components/debug-page"
)

// fakeMediad — поддельный штатный mediad: принимает соединения и складывает пришедшие байты в канал.
func fakeMediad(t *testing.T) (addr string, got <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan []byte, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						ch <- append([]byte(nil), buf[:n]...)
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), ch
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func dialRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("шим не слушает %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDaemonPassesFramesAndAnswersControl(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "openmediad.conf")
	os.WriteFile(conf, []byte("[general]\nlog_level = \"trace\"\n[debug-page]\nlisten = \"127.0.0.1:0\"\n"), 0o644)
	upstream, got := fakeMediad(t)
	o := Options{ConfigPath: conf, Listen: freeAddr(t), Upstream: upstream, Src: "", Socket: filepath.Join(dir, "ctl.sock")}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	// кадр платформы, который openmediad не берёт себе, доходит до mediad байт в байт
	cli := dialRetry(t, o.Listen)
	defer cli.Close()
	frame := wire.AppendFrame(nil, wire.Wrap(wire.TypeIOHub, []byte{0x08, 0x01}))
	cli.Write(frame)
	var recv []byte
	timeout := time.After(3 * time.Second)
	for len(recv) < len(frame) {
		select {
		case b := <-got:
			recv = append(recv, b...)
		case <-timeout:
			t.Fatalf("mediad не получил кадр (пришло %x)", recv)
		}
	}
	if !bytes.Equal(recv, frame) {
		t.Fatalf("mediad получил %x, ждали %x", recv, frame)
	}

	var st ctlsock.Status
	if err := ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "status", Detail: true}, &st); err != nil {
		t.Fatal(err)
	}
	if st.Connections != 1 || !st.Voice || st.Music || st.LogLevel != "trace" {
		t.Errorf("status: %+v", st)
	}
	running := map[string]bool{}
	for _, c := range st.Components {
		running[c.Name] = c.Running
	}
	if !running["debug-page"] || !running["alice-voice"] {
		t.Errorf("компоненты: %+v", st.Components)
	}

	var es []journal.Entry
	if err := ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "logs"}, &es); err != nil {
		t.Fatal(err)
	}
	if !hasEntry(es, "shim", "") || !hasEntry(es, "system", "компоненты") {
		t.Errorf("в журнале нет записей шима или запуска: %d записей", len(es))
	}

	// второй демон на том же сокете не встаёт
	o2 := o
	o2.Listen = freeAddr(t)
	if err := Run(context.Background(), o2); err == nil || !strings.Contains(err.Error(), "сокет") {
		t.Errorf("второй демон: %v, ждали отказ по сокету", err)
	}

	// перечитывание: уровень журнала применяется на ходу, выключенный компонент — после перезапуска
	os.WriteFile(conf, []byte("[general]\nlog_level = \"warn\"\n[alice-voice]\nenabled = false\n[debug-page]\nlisten = \"127.0.0.1:0\"\n"), 0o644)
	var msg string
	if err := ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "reload"}, &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "компонент alice-voice") {
		t.Errorf("reload: %q — ждали упоминание alice-voice", msg)
	}
	ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "status"}, &st)
	if st.LogLevel != "warn" {
		t.Errorf("уровень после reload: %s", st.LogLevel)
	}

	// неверные настройки не применяются
	os.WriteFile(conf, []byte("[general]\nlog_level = \"громко\"\n"), 0o644)
	if err := ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "reload"}, &msg); err == nil {
		t.Error("reload с неверными настройками прошёл")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("демон не остановился")
	}
	if _, err := os.Stat(o.Socket); err == nil {
		if err := ctlsock.Call(o.Socket, ctlsock.Request{Cmd: "status"}, nil); err == nil {
			t.Error("сокет отвечает после остановки")
		}
	}
	io.Copy(io.Discard, cli)
}

func hasEntry(es []journal.Entry, src, sub string) bool {
	for _, e := range es {
		if e.Src == src && strings.Contains(e.Msg, sub) {
			return true
		}
	}
	return false
}

func TestReplaceModeRefused(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "c.conf")
	os.WriteFile(conf, []byte("[general]\nmode = \"replace\"\n"), 0o644)
	err := Run(context.Background(), Options{ConfigPath: conf, Socket: filepath.Join(dir, "s")})
	if err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("ждали отказ режима replace, получили %v", err)
	}
}

func TestDedupSkipsCopiesWithinWindow(t *testing.T) {
	var d dedup
	f := []byte("событие")
	if d.seen(f) {
		t.Fatal("первая копия считается повтором")
	}
	for i := 0; i < 3; i++ {
		if !d.seen(f) {
			t.Fatal("копия рассылки не узнана")
		}
	}
	if d.seen([]byte("другое")) {
		t.Fatal("другой кадр считается повтором")
	}
}
