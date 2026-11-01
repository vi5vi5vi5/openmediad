// Package wsclient — минимальный клиент WebSocket (RFC 6455) поверх обычного TCP: рукопожатие, текстовые сообщения, ping/pong,
// закрытие. Без сжатия и без TLS (нужен локальным соединениям, например с WebSocket API Spotify Soloist на 127.0.0.1).
//
// Читать (ReadMessage) должна одна горутина; писать (WriteText) можно из нескольких.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	guid       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessage = 4 << 20 // самое большое сообщение, которое клиент согласен принять

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
}

// Dial открывает соединение с addr («127.0.0.1:9090») и просит перейти на WebSocket по пути path («/»).
func Dial(ctx context.Context, addr, path string) (*Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := handshake(ctx, c, addr, path)
	if err != nil {
		c.Close()
		return nil, err
	}
	return conn, nil
}

func handshake(ctx context.Context, c net.Conn, host, path string) (*Conn, error) {
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
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host, key)
	if _, err := io.WriteString(c, req); err != nil {
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
	return &Conn{c: c, br: br}, nil
}

// SetReadDeadline задаёт срок для следующего ReadMessage.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// WriteText отправляет текстовое сообщение.
func (c *Conn) WriteText(s string) error { return c.writeFrame(opText, []byte(s)) }

// Close отправляет закрытие и разрывает соединение.
func (c *Conn) Close() error {
	c.c.SetWriteDeadline(time.Now().Add(time.Second))
	c.writeFrame(opClose, []byte{0x03, 0xE8}) // 1000: штатное закрытие
	return c.c.Close()
}

func (c *Conn) writeFrame(op byte, payload []byte) error {
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
	_, err := c.c.Write(buf)
	return err
}

// ReadMessage читает следующее текстовое или двоичное сообщение целиком (склеивает фрагменты), отвечает на ping и сообщает о
// закрытии как io.EOF.
func (c *Conn) ReadMessage() ([]byte, error) {
	var msg []byte
	inMsg := false
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opClose:
			c.writeFrame(opClose, payload)
			return nil, io.EOF
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opText, opBin:
			if inMsg {
				return nil, errors.New("ws: новое сообщение посреди фрагментов")
			}
			inMsg = true
			msg = payload
		case opCont:
			if !inMsg {
				return nil, errors.New("ws: продолжение без начала")
			}
			if len(msg)+len(payload) > maxMessage {
				return nil, errors.New("ws: сообщение слишком большое")
			}
			msg = append(msg, payload...)
		default:
			return nil, fmt.Errorf("ws: неизвестный код кадра 0x%x", op)
		}
		if fin {
			return msg, nil
		}
	}
}

func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
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
	n := uint64(h[1] & 0x7F)
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
	if n > maxMessage {
		err = errors.New("ws: кадр слишком большой")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}
