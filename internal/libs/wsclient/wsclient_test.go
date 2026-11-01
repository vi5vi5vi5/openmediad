package wsclient

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// server — поддельный сервер WebSocket: принимает одно соединение, проходит рукопожатие и отдаёт кадры тесту.
type server struct {
	ln net.Listener
	c  net.Conn
	br *bufio.Reader
}

func newServer(t *testing.T, badAccept bool) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{ln: ln}
	t.Cleanup(func() {
		ln.Close()
		if s.c != nil {
			s.c.Close()
		}
	})
	return s
}

func (s *server) accept(t *testing.T, badAccept bool) {
	t.Helper()
	c, err := s.ln.Accept()
	if err != nil {
		t.Error(err)
		return
	}
	s.c, s.br = c, bufio.NewReader(c)
	req, err := http.ReadRequest(s.br)
	if err != nil {
		t.Error(err)
		return
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + guid))
	acc := base64.StdEncoding.EncodeToString(sum[:])
	if badAccept {
		acc = "нет"
	}
	io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+acc+"\r\n\r\n")
}

// send пишет кадр без маски (серверные кадры не маскируются).
func (s *server) send(fin bool, op byte, payload []byte) {
	h := []byte{op}
	if fin {
		h[0] |= 0x80
	}
	switch l := len(payload); {
	case l < 126:
		h = append(h, byte(l))
	case l <= 0xFFFF:
		h = append(h, 126, byte(l>>8), byte(l))
	default:
		h = append(h, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(l))
		h = append(h, b[:]...)
	}
	s.c.Write(append(h, payload...))
}

// recv читает один клиентский кадр (он должен быть с маской).
func (s *server) recv(t *testing.T) (op byte, payload []byte) {
	t.Helper()
	s.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(s.br, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[1]&0x80 == 0 {
		t.Fatal("клиентский кадр без маски")
	}
	n := int(h[1] & 0x7F)
	if n == 126 {
		var b [2]byte
		io.ReadFull(s.br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	}
	var mask [4]byte
	io.ReadFull(s.br, mask[:])
	payload = make([]byte, n)
	io.ReadFull(s.br, payload)
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return h[0] & 0x0F, payload
}

func dial(t *testing.T, s *server, badAccept bool) (*Conn, error) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.accept(t, badAccept); close(done) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := Dial(ctx, s.ln.Addr().String(), "/")
	<-done
	return c, err
}

func TestMessagesPingFragmentsClose(t *testing.T) {
	s := newServer(t, false)
	c, err := dial(t, s, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// обычное сообщение, длинное (кадр с 16-битной длиной) и фрагментированное, между фрагментами — ping
	long := strings.Repeat("я", 300)
	s.send(true, opText, []byte(`{"type":"auth_state"}`))
	s.send(true, opText, []byte(long))
	s.send(false, opText, []byte("часть "))
	s.send(true, opPing, []byte("p"))
	s.send(true, opCont, []byte("вторая"))

	for _, want := range []string{`{"type":"auth_state"}`, long, "часть вторая"} {
		got, err := c.ReadMessage()
		if err != nil || string(got) != want {
			t.Fatalf("получено %q, %v; ждали %q", got, err, want)
		}
	}
	if op, p := s.recv(t); op != opPong || string(p) != "p" {
		t.Fatalf("на ping ответ: код %x, %q", op, p)
	}

	if err := c.WriteText(`{"type":"command","command":"pause"}`); err != nil {
		t.Fatal(err)
	}
	if op, p := s.recv(t); op != opText || string(p) != `{"type":"command","command":"pause"}` {
		t.Fatalf("клиент прислал: код %x, %q", op, p)
	}

	s.send(true, opClose, []byte{0x03, 0xE8})
	if _, err := c.ReadMessage(); err != io.EOF {
		t.Fatalf("после close: %v, ждали io.EOF", err)
	}
}

func TestHandshakeErrors(t *testing.T) {
	s := newServer(t, true)
	if _, err := dial(t, s, true); err == nil || !strings.Contains(err.Error(), "Accept") {
		t.Fatalf("неверный Accept принят: %v", err)
	}
	// не WebSocket: сервер отвечает 200
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		http.ReadRequest(bufio.NewReader(c))
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), "/"); err == nil || !strings.Contains(err.Error(), "101") {
		t.Fatalf("ответ 200 принят: %v", err)
	}
	if _, err := Dial(context.Background(), "127.0.0.1:1", "/"); err == nil {
		t.Fatal("соединение с закрытым портом открылось")
	}
}

func TestReadErrors(t *testing.T) {
	s := newServer(t, false)
	c, err := dial(t, s, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s.send(true, opCont, []byte("x")) // продолжение без начала
	if _, err := c.ReadMessage(); err == nil {
		t.Fatal("продолжение без начала принято")
	}
}
