package spotifysoloist

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
)

// waitFor ждёт условие до 3 секунд.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

// ---------- вывод в память и движок с ним ----------

type memOut struct {
	mu      sync.Mutex
	n       int
	aborted atomic.Bool
	drained atomic.Bool
}

func (o *memOut) Write(p []byte) (int, error) {
	if o.aborted.Load() {
		return 0, errors.New("вывод оборван")
	}
	o.mu.Lock()
	o.n += len(p)
	o.mu.Unlock()
	return len(p), nil
}
func (o *memOut) Drain() { o.drained.Store(true) }
func (o *memOut) Abort() { o.aborted.Store(true) }
func (o *memOut) bytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.n
}

type outs struct {
	mu   sync.Mutex
	list []*memOut
}

func (o *outs) all() []*memOut {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*memOut(nil), o.list...)
}

func (o *outs) total() (n int) {
	for _, m := range o.all() {
		n += m.bytes()
	}
	return n
}

func newEngine() (*engine.Engine, *outs) {
	o := &outs{}
	return &engine.Engine{Router: engine.RouterFunc(func(c engine.Class, f audio.Format) (audio.Output, error) {
		m := &memOut{}
		o.mu.Lock()
		o.list = append(o.list, m)
		o.mu.Unlock()
		return m, nil
	})}, o
}

// ---------- кадры шима ----------

func hello(rate uint32, ch byte) []byte {
	h := []byte{'O', 'M', 'D', 'P', 1, ch, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(h[6:], rate)
	return h
}

func frame(typ byte, payload []byte) []byte {
	h := make([]byte, 5, 5+len(payload))
	h[0] = typ
	binary.LittleEndian.PutUint32(h[1:], uint32(len(payload)))
	return append(h, payload...)
}

func cork(on bool) []byte {
	if on {
		return frame(frCork, []byte{1})
	}
	return frame(frCork, []byte{0})
}

func pcm(frames int) []byte { return make([]byte, frames*4) } // 2 канала S16

// ---------- поддельный WebSocket-сервер (Soloist) ----------

type wsServer struct {
	ln    net.Listener
	mu    sync.Mutex
	c     net.Conn
	recv  chan string
	conns atomic.Int32 // сколько соединений принято
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func startWS(t testing.TB) *wsServer {
	t.Helper()
	s, err := listenWS("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ln.Close() })
	return s
}

// listenWS открывает поддельный WebSocket-сервер на addr (им же пользуется поддельный Soloist в отдельном процессе).
func listenWS(addr string) (*wsServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &wsServer{ln: ln, recv: make(chan string, 32)}
	go s.serve()
	return s, nil
}

func (s *wsServer) addr() string { return s.ln.Addr().String() }

func (s *wsServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.conn(c)
	}
}

func (s *wsServer) conn(c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		c.Close()
		return
	}
	sum := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + wsGUID))
	io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
		base64.StdEncoding.EncodeToString(sum[:])+"\r\n\r\n")
	s.mu.Lock()
	s.c = c
	s.mu.Unlock()
	s.conns.Add(1)
	for {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return
		}
		n := int(h[1] & 0x7F)
		if n == 126 {
			var b [2]byte
			io.ReadFull(br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		}
		var mask [4]byte
		io.ReadFull(br, mask[:])
		p := make([]byte, n)
		if _, err := io.ReadFull(br, p); err != nil {
			return
		}
		for i := range p {
			p[i] ^= mask[i%4]
		}
		if h[0]&0x0F == 0x1 {
			select {
			case s.recv <- string(p):
			default:
			}
		}
	}
}

// push отправляет клиенту текстовое сообщение.
func (s *wsServer) push(t testing.TB, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		c := s.c
		s.mu.Unlock()
		if c != nil {
			p := []byte(text)
			h := []byte{0x81}
			switch l := len(p); {
			case l < 126:
				h = append(h, byte(l))
			default:
				h = append(h, 126, byte(l>>8), byte(l))
			}
			if _, err := c.Write(append(h, p...)); err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("клиент WebSocket не подключился")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
