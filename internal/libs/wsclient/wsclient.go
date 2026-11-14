// Package wsclient — минимальный клиент WebSocket (RFC 6455): рукопожатие, текстовые и двоичные сообщения, ping/pong, закрытие.
// Без сжатия. Dial — обычный TCP (локальные соединения, например с WebSocket API Spotify Soloist на 127.0.0.1); DialURL — ещё и
// TLS (wss://), свои заголовки рукопожатия, срок записи и пропуск двоичных сообщений без выделения памяти.
//
// Читать (ReadMessage, Read) должна одна горутина; писать (WriteText, WriteBinary) можно из нескольких.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	guid       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessage = 4 << 20 // самое большое сообщение, которое клиент согласен принять (другое — Options.MaxMessage)

	opCont  = 0x0
	opText  = 0x1
	opBin   = 0x2
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

// Conn — соединение WebSocket.
type Conn struct {
	c   net.Conn
	br  *bufio.Reader
	wmu sync.Mutex

	maxMessage   int           // предел размера принимаемого сообщения
	writeTimeout time.Duration // срок одной записи (0 — без срока)
	skipBinary   bool          // двоичные сообщения выбрасываются при чтении
	skipping     bool          // идёт выбрасываемое сообщение из нескольких фрагментов
	skippedMsgs  atomic.Uint64
	skippedBytes atomic.Uint64
}

// Options — необязательные параметры DialURL.
type Options struct {
	// Header — дополнительные заголовки рукопожатия (например, Cookie).
	Header http.Header
	// TLS — настройки TLS для wss:// (nil — по умолчанию: системные корневые сертификаты, имя сервера из адреса).
	TLS *tls.Config
	// WriteTimeout — срок одной записи: соединение, которое перестало принимать данные, не держит пишущего дольше (0 — без срока).
	WriteTimeout time.Duration
	// SkipBinary — двоичные сообщения читаются из сети и выбрасываются, не попадая в память целиком: Read их не возвращает.
	// Для клиента, которому сервер шлёт большой двоичный поток (видео), а нужен только текст. Сколько выброшено — Skipped.
	SkipBinary bool
	// MaxMessage — самое большое сообщение, которое клиент согласен принять целиком (0 — 4 МБ).
	MaxMessage int
}

// Dial открывает соединение с addr («127.0.0.1:9090») и просит перейти на WebSocket по пути path («/»).
func Dial(ctx context.Context, addr, path string) (*Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := handshake(ctx, c, addr, path, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	return conn, nil
}

// DialURL открывает соединение по адресу вида ws://хост[:порт]/путь или wss://хост[:порт]/путь. Имя хоста должно быть в ASCII
// (для доменов с национальными буквами — в виде xn--…).
func DialURL(ctx context.Context, rawurl string, o *Options) (*Conn, error) {
	if o == nil {
		o = &Options{}
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, fmt.Errorf("ws: адрес %q: %w", rawurl, err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("ws: в адресе %q нет хоста", rawurl)
	}
	port := u.Port()
	var c net.Conn
	switch u.Scheme {
	case "ws":
		if port == "" {
			port = "80"
		}
		var d net.Dialer
		c, err = d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	case "wss":
		if port == "" {
			port = "443"
		}
		d := tls.Dialer{Config: o.TLS}
		c, err = d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	default:
		return nil, fmt.Errorf("ws: схема %q не поддерживается (нужна ws или wss)", u.Scheme)
	}
	if err != nil {
		return nil, err
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	conn, err := handshake(ctx, c, u.Host, path, o.Header)
	if err != nil {
		c.Close()
		return nil, err
	}
	conn.writeTimeout, conn.skipBinary = o.WriteTimeout, o.SkipBinary
	if o.MaxMessage > 0 {
		conn.maxMessage = o.MaxMessage
	}
	return conn, nil
}

func handshake(ctx context.Context, c net.Conn, host, path string, extra http.Header) (*Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	c.SetDeadline(deadline)
	defer c.SetDeadline(time.Time{})

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(raw[:])
	var req strings.Builder
	fmt.Fprintf(&req, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", path, host, key)
	for name, values := range extra {
		for _, v := range values {
			if strings.ContainsAny(name+v, "\r\n") {
				return nil, fmt.Errorf("ws: перевод строки в заголовке %q", name)
			}
			fmt.Fprintf(&req, "%s: %s\r\n", name, v)
		}
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(c, req.String()); err != nil {
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("ws: ответ на рукопожатие: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("ws: сервер ответил %s вместо 101", resp.Status)
	}
	sum := sha1.Sum([]byte(key + guid))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return nil, errors.New("ws: неверный Sec-WebSocket-Accept")
	}
	return &Conn{c: c, br: br, maxMessage: maxMessage}, nil
}

// SetReadDeadline задаёт срок для следующего ReadMessage.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// WriteText отправляет текстовое сообщение.
func (c *Conn) WriteText(s string) error { return c.writeFrame(opText, []byte(s)) }

// WriteBinary отправляет двоичное сообщение.
func (c *Conn) WriteBinary(p []byte) error { return c.writeFrame(opBin, p) }

// Skipped — сколько двоичных сообщений и байт выброшено при чтении (Options.SkipBinary).
func (c *Conn) Skipped() (messages, bytes uint64) { return c.skippedMsgs.Load(), c.skippedBytes.Load() }

// Close отправляет закрытие и разрывает соединение.
func (c *Conn) Close() error {
	c.c.SetWriteDeadline(time.Now().Add(time.Second)) // запись, застрявшая в другой горутине, отпустит замок
	c.write(opClose, []byte{0x03, 0xE8}, time.Second) // 1000: штатное закрытие
	return c.c.Close()
}

func (c *Conn) writeFrame(op byte, payload []byte) error { return c.write(op, payload, c.writeTimeout) }

// write отправляет один кадр; timeout — срок записи (0 — без срока).
func (c *Conn) write(op byte, payload []byte, timeout time.Duration) error {
	var h [14]byte
	h[0] = 0x80 | op
	n := 2
	switch l := len(payload); {
	case l < 126:
		h[1] = 0x80 | byte(l)
	case l <= 0xFFFF:
		h[1] = 0x80 | 126
		binary.BigEndian.PutUint16(h[2:], uint16(l))
		n = 4
	default:
		h[1] = 0x80 | 127
		binary.BigEndian.PutUint64(h[2:], uint64(l))
		n = 10
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(h[n:], mask[:])
	n += 4
	buf := make([]byte, 0, n+len(payload))
	buf = append(buf, h[:n]...)
	for i, b := range payload {
		buf = append(buf, b^mask[i%4])
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if timeout > 0 {
		c.c.SetWriteDeadline(time.Now().Add(timeout))
	}
	_, err := c.c.Write(buf)
	return err
}

// ReadMessage читает следующее текстовое или двоичное сообщение целиком (склеивает фрагменты), отвечает на ping и сообщает о
// закрытии как io.EOF.
func (c *Conn) ReadMessage() ([]byte, error) {
	_, msg, err := c.Read()
	return msg, err
}

// Read — то же, что ReadMessage, но говорит, двоичное ли сообщение. С Options.SkipBinary двоичные сообщения не возвращаются.
func (c *Conn) Read() (bin bool, msg []byte, err error) {
	inMsg := false
	for {
		fin, op, n, mask, err := c.readHeader()
		if err != nil {
			return false, nil, err
		}
		if c.skipBinary && (op == opBin && !inMsg || op == opCont && c.skipping) {
			if err := c.discard(n); err != nil {
				return false, nil, err
			}
			c.skippedBytes.Add(n)
			if c.skipping = !fin; fin {
				c.skippedMsgs.Add(1)
			}
			continue
		}
		if n > uint64(c.maxMessage) {
			return false, nil, errors.New("ws: кадр слишком большой")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return false, nil, err
		}
		for i := range payload {
			if mask == nil {
				break
			}
			payload[i] ^= mask[i%4]
		}
		switch op {
		case opClose:
			c.writeFrame(opClose, payload)
			return false, nil, io.EOF
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return false, nil, err
			}
			continue
		case opPong:
			continue
		case opText, opBin:
			if inMsg || c.skipping {
				return false, nil, errors.New("ws: новое сообщение посреди фрагментов")
			}
			inMsg, bin = true, op == opBin
			msg = payload
		case opCont:
			if !inMsg {
				return false, nil, errors.New("ws: продолжение без начала")
			}
			if len(msg)+len(payload) > c.maxMessage {
				return false, nil, errors.New("ws: сообщение слишком большое")
			}
			msg = append(msg, payload...)
		default:
			return false, nil, fmt.Errorf("ws: неизвестный код кадра 0x%x", op)
		}
		if fin {
			return bin, msg, nil
		}
	}
}

// discard читает и выбрасывает n байт данных кадра.
func (c *Conn) discard(n uint64) error {
	for n > 0 {
		d, err := c.br.Discard(int(min(n, 1<<20)))
		n -= uint64(d)
		if err != nil {
			return err
		}
	}
	return nil
}

// readHeader читает заголовок кадра: длину данных n и маску (nil — данные без маски). Сами данные читает вызывающий.
func (c *Conn) readHeader() (fin bool, op byte, n uint64, mask []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0F
	if h[0]&0x70 != 0 {
		err = errors.New("ws: занятые зарезервированные биты (сжатие не поддержано)")
		return
	}
	masked := h[1]&0x80 != 0
	n = uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= opClose && (!fin || n > 125) {
		err = errors.New("ws: повреждён управляющий кадр")
		return
	}
	if masked {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(c.br, mask); err != nil {
			return
		}
	}
	return
}
