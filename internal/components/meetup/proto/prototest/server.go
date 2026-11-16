// Package prototest — поддельный сервер MeetUp для тестов: принимает WebSocket, отвечает на join, возвращает чат всем,
// запоминает состояние «микрофона» и звуковые кадры; умеет аккаунты (вход по логину и паролю, кука сессии) и пароль комнаты. Говорит на том же протоколе, что настоящий (Server/docs в репозитории
// MeetUp), ровно в объёме, который нужен клиенту колонки.
package prototest

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Frame — звуковой кадр, присланный клиентом.
type Frame struct {
	Type, Flags, Codec byte
	TimeMS             uint64
	Payload            []byte
}

// Server — поддельный сервер.
type Server struct {
	// Chats — тексты, которые клиент написал в чат; Mic — присланные состояния микрофона; Audio — звуковые кадры.
	Chats chan string
	Mic   chan bool
	Audio chan Frame

	srv *httptest.Server

	mu          sync.Mutex
	conns       map[*conn]bool
	joins       []map[string]any
	joinError   string
	nextID      uint32
	accounts    map[string]*account // по логину
	tokens      map[string]*account // по куке сессии
	cookies     int                 // сколько рукопожатий WebSocket пришло с кукой сессии
	password    string              // пароль комнаты ("" — без пароля)
	accountOnly bool                // сервер «только для своих»: без аккаунта не пускает
}

type account struct {
	id                    int64
	login, password, name string
}

type conn struct {
	c    net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
	id   uint32
	name string
	acc  *account // аккаунт из куки рукопожатия (nil — без аккаунта)
}

// New запускает сервер; он останавливается вместе с тестом.
func New(t testing.TB) *Server {
	s := &Server{Chats: make(chan string, 64), Mic: make(chan bool, 64), Audio: make(chan Frame, 1024), conns: map[*conn]bool{}, nextID: 7,
		accounts: map[string]*account{}, tokens: map[string]*account{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		s.Drop()
		s.srv.Close()
	})
	return s
}

// Link — ссылка на комнату этого сервера, как её скопировал бы человек.
func (s *Server) Link(room string) string { return s.srv.URL + "/conference.html?room=" + room }

// WSURL — адрес WebSocket сервера.
func (s *Server) WSURL() string { return "ws://" + strings.TrimPrefix(s.srv.URL, "http://") + "/ws" }

// AddAccount заводит аккаунт и возвращает его id.
func (s *Server) AddAccount(login, password, name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &account{id: int64(len(s.accounts) + 5), login: login, password: password, name: name}
	s.accounts[login] = a
	return a.id
}

// SessionCookies — сколько раз клиент прислал куку сессии при подключении (действующую или нет): чужому серверу кука
// уходить не должна.
func (s *Server) SessionCookies() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cookies
}

// ExpireSessions делает недействительными все выданные куки сессий (сессия истекла на сервере).
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	s.tokens = map[string]*account{}
	s.mu.Unlock()
}

// RequirePassword задаёт пароль комнаты ("" — без пароля).
func (s *Server) RequirePassword(password string) {
	s.mu.Lock()
	s.password = password
	s.mu.Unlock()
}

// RequireAccount включает режим «только для своих»: вход без аккаунта отвергается.
func (s *Server) RequireAccount(on bool) {
	s.mu.Lock()
	s.accountOnly = on
	s.mu.Unlock()
}

// api — HTTP API аккаунтов: вход и выход.
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/login":
		var in struct{ Login, Password string }
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			reply(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
			return
		}
		s.mu.Lock()
		a := s.accounts[strings.ToLower(strings.TrimSpace(in.Login))]
		ok := a != nil && a.password == in.Password
		token := ""
		if ok {
			token = fmt.Sprintf("session-%s-%d", a.login, len(s.tokens)+1)
			s.tokens[token] = a
		}
		s.mu.Unlock()
		if !ok {
			reply(http.StatusUnauthorized, map[string]string{"error": "wrong_credentials"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "meetup_session", Value: token, Path: "/", HttpOnly: true, MaxAge: 2592000})
		reply(http.StatusOK, map[string]any{"user": map[string]any{"id": a.id, "login": a.login, "display_name": a.name, "avatar_ver": 0}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/logout":
		if ck, err := r.Cookie("meetup_session"); err == nil {
			s.mu.Lock()
			delete(s.tokens, ck.Value)
			s.mu.Unlock()
		}
		reply(http.StatusOK, map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

// RefuseJoin заставляет сервер отвечать на join ошибкой с причиной reason ("" — снова пускать).
func (s *Server) RefuseJoin(reason string) {
	s.mu.Lock()
	s.joinError = reason
	s.mu.Unlock()
}

// Joins — сообщения join, которые сервер получил (включая отвергнутые).
func (s *Server) Joins() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.joins...)
}

// Clients — сколько клиентов сейчас в комнате.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Say рассылает всем сообщение чата от участника id по имени name.
func (s *Server) Say(id uint32, name, text string) {
	s.Broadcast(map[string]any{"type": "chat", "sender_id": id, "sender_name": name, "text": text, "timestamp_ms": time.Now().UnixMilli()})
}

// Broadcast рассылает всем текстовое сообщение.
func (s *Server) Broadcast(msg map[string]any) {
	b, _ := json.Marshal(msg)
	for _, c := range s.list() {
		c.write(0x1, b)
	}
}

// SendBinary рассылает всем двоичный кадр (например, чужое видео).
func (s *Server) SendBinary(p []byte) {
	for _, c := range s.list() {
		c.write(0x2, p)
	}
}

// Drop рвёт все соединения (сеть пропала).
func (s *Server) Drop() {
	for _, c := range s.list() {
		c.c.Close()
	}
}

func (s *Server) list() []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.api(w, r)
		return
	}
	var acc *account
	if ck, err := r.Cookie("meetup_session"); err == nil {
		s.mu.Lock()
		acc = s.tokens[ck.Value]
		s.cookies++
		s.mu.Unlock()
	}
	nc, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer nc.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	rw.Flush()
	c := &conn{c: nc, br: rw.Reader, acc: acc}
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	for {
		op, payload, err := c.read()
		if err != nil {
			return
		}
		switch op {
		case 0x1:
			s.text(c, payload)
		case 0x2:
			if len(payload) >= 11 {
				f := Frame{Type: payload[0], Flags: payload[1], Codec: payload[2], TimeMS: binary.LittleEndian.Uint64(payload[3:11]), Payload: payload[11:]}
				select {
				case s.Audio <- f:
				default:
				}
			}
		case 0x8:
			return
		case 0x9:
			c.write(0xA, payload)
		}
	}
}

func (s *Server) text(c *conn, raw []byte) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		c.json(map[string]any{"type": "error", "reason": "invalid_json"})
		return
	}
	switch m["type"] {
	case "join":
		s.mu.Lock()
		s.joins = append(s.joins, m)
		reason := s.joinError
		pw, _ := m["password"].(string)
		switch {
		case reason != "":
		case c.acc == nil && s.accountOnly:
			reason = "anonymous_forbidden"
		case s.password != "" && pw != s.password:
			reason = "wrong_password"
		case c.acc != nil: // имя из профиля, id постоянный (§2.4)
			c.id, c.name = 0x80000000+uint32(c.acc.id), c.acc.name
			s.conns[c] = true
		default:
			c.name, _ = m["name"].(string)
			c.id = s.nextID
			s.nextID++
			s.conns[c] = true
		}
		s.mu.Unlock()
		if reason != "" {
			c.json(map[string]any{"type": "error", "reason": reason})
			return
		}
		// В истории лежит команда: клиент не должен исполнять то, что было сказано до его входа.
		c.json(map[string]any{"type": "join_ok", "sender_id": c.id, "features": []string{"recv", "demand", "kind"},
			"participants": []map[string]any{{"id": 3, "name": "Анна", "mic": true, "cam": false, "user_id": 5, "avatar": 2}},
			"history": []map[string]any{{"sender_id": 3, "sender_name": "Анна", "text": "!pause", "timestamp_ms": 1751970000000,
				"image": strings.Repeat("A", 4096)}}})
	case "chat":
		text, _ := m["text"].(string)
		select {
		case s.Chats <- text:
		default:
		}
		s.Say(c.id, c.name, text)
	case "state":
		if on, ok := m["mic"].(bool); ok {
			select {
			case s.Mic <- on:
			default:
			}
		}
	case "ping":
		c.json(map[string]any{"type": "pong", "t": m["t"]})
	default:
		c.json(map[string]any{"type": "error", "reason": "unknown_type"})
	}
}

func (c *conn) json(m map[string]any) {
	b, _ := json.Marshal(m)
	c.write(0x1, b)
}

// write отправляет кадр без маски (серверные кадры не маскируются).
func (c *conn) write(op byte, payload []byte) {
	h := []byte{0x80 | op}
	switch l := len(payload); {
	case l < 126:
		h = append(h, byte(l))
	case l <= 0xFFFF:
		h = append(h, 126, byte(l>>8), byte(l))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(l))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	c.c.Write(append(h, payload...))
}

// read читает один клиентский кадр (с маской; фрагменты клиент колонки не шлёт).
func (c *conn) read() (op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
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
	var mask [4]byte
	if h[1]&0x80 != 0 {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return h[0] & 0x0F, payload, nil
}
