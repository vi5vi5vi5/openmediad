package dbus

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SystemBus — сокет системной шины колонки (другой — DBUS_SYSTEM_BUS_ADDRESS вида unix:path=…).
const SystemBus = "/run/dbus/system_bus_socket"

// Error — ответ-ошибка шины или сервиса (org.bluez.Error.Failed и т. п.).
type Error struct {
	Name    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Name
	}
	return e.Name + ": " + e.Message
}

// ErrClosed — соединение закрыто (шина перезапустилась или Close).
var ErrClosed = errors.New("dbus: соединение закрыто")

// Conn — соединение с шиной. Методы можно вызывать из разных горутин.
type Conn struct {
	rw io.ReadWriteCloser
	r  *bufio.Reader

	wmu sync.Mutex // запись сообщения — целиком

	mu      sync.Mutex
	serial  uint32
	pending map[uint32]chan *Message
	subs    map[*Subscription]bool
	err     error

	done chan struct{}
	name string // уникальное имя на шине (из Hello)
}

// DialSystem подключается к системной шине.
func DialSystem() (*Conn, error) {
	path := SystemBus
	if a := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
		p, ok := strings.CutPrefix(a, "unix:path=")
		if !ok {
			return nil, fmt.Errorf("dbus: адрес %q не поддерживается (нужен unix:path=…)", a)
		}
		path, _, _ = strings.Cut(p, ",")
	}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("системная шина D-Bus: %w", err)
	}
	return NewConn(c, os.Getuid())
}

// NewConn проходит вход (EXTERNAL с uid) и представляется шине (Hello). rw закрывается при ошибке и в Close.
func NewConn(rw io.ReadWriteCloser, uid int) (*Conn, error) {
	c := &Conn{rw: rw, r: bufio.NewReader(rw), pending: map[uint32]chan *Message{}, subs: map[*Subscription]bool{}, done: make(chan struct{})}
	if err := c.auth(uid); err != nil {
		rw.Close()
		return nil, err
	}
	go c.readLoop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "")
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("dbus: Hello: %w", err)
	}
	if len(body) == 1 {
		c.name, _ = body[0].(string)
	}
	return c, nil
}

// auth — вход по протоколу SASL шины: нулевой байт, AUTH EXTERNAL <uid в hex>, ответ OK, BEGIN.
func (c *Conn) auth(uid int) error {
	if uid < 0 {
		uid = 0
	}
	id := hex.EncodeToString([]byte(strconv.Itoa(uid)))
	if _, err := io.WriteString(c.rw, "\x00AUTH EXTERNAL "+id+"\r\n"); err != nil {
		return fmt.Errorf("dbus: вход: %w", err)
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("dbus: вход: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("dbus: шина не пустила: %q", strings.TrimSpace(line))
	}
	if _, err := io.WriteString(c.rw, "BEGIN\r\n"); err != nil {
		return fmt.Errorf("dbus: вход: %w", err)
	}
	return nil
}

// Name — уникальное имя соединения на шине (например «:1.42»).
func (c *Conn) Name() string { return c.name }

// Done закрывается, когда соединение умерло; причина — Err.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err — почему соединение закрыто (nil, пока живо).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close закрывает соединение. Ожидающие вызовы получают ErrClosed, каналы подписок закрываются.
func (c *Conn) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	pending := c.pending
	c.pending = map[uint32]chan *Message{}
	subs := c.subs
	c.subs = map[*Subscription]bool{}
	c.mu.Unlock()
	c.rw.Close()
	for _, ch := range pending {
		close(ch)
	}
	for s := range subs {
		s.closeCh()
	}
	close(c.done)
}

func (c *Conn) send(m *Message) (uint32, chan *Message, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, nil, err
	}
	c.serial++
	if c.serial == 0 {
		c.serial = 1
	}
	m.Serial = c.serial
	var ch chan *Message
	if m.Type == TypeMethodCall && m.Flags&FlagNoReplyExpected == 0 {
		ch = make(chan *Message, 1)
		c.pending[m.Serial] = ch
	}
	c.mu.Unlock()
	b, err := m.Marshal()
	if err == nil {
		c.wmu.Lock()
		_, err = c.rw.Write(b)
		c.wmu.Unlock()
		if err != nil {
			c.fail(fmt.Errorf("dbus: запись: %w", err))
		}
	}
	if err != nil {
		c.forget(m.Serial)
		return 0, nil, err
	}
	return m.Serial, ch, nil
}

func (c *Conn) forget(serial uint32) {
	c.mu.Lock()
	delete(c.pending, serial)
	c.mu.Unlock()
}

// Call вызывает метод и ждёт ответа (или отмены ctx). sig — подпись аргументов, args — значения по ней.
func (c *Conn) Call(ctx context.Context, dest string, path ObjectPath, iface, member, sig string, args ...any) ([]any, error) {
	m := &Message{Type: TypeMethodCall, Destination: dest, Path: path, Interface: iface, Member: member, Signature: sig, Body: args}
	serial, ch, err := c.send(m)
	if err != nil {
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, c.Err()
		}
		if r.Type == TypeError {
			e := &Error{Name: r.ErrorName}
			if len(r.Body) > 0 {
				e.Message, _ = r.Body[0].(string)
			}
			return nil, e
		}
		return r.Body, nil
	case <-ctx.Done():
		c.forget(serial)
		return nil, fmt.Errorf("dbus: %s.%s: %w", iface, member, ctx.Err())
	}
}

// Emit отправляет сигнал. dest — адресат (уникальное имя соединения): тогда сигнал получит только он; пусто — всем,
// кто подписан.
func (c *Conn) Emit(dest string, path ObjectPath, iface, member, sig string, args ...any) error {
	_, _, err := c.send(&Message{Type: TypeSignal, Destination: dest, Path: path, Interface: iface, Member: member, Signature: sig, Body: args})
	return err
}

// readLoop разбирает входящие сообщения: ответы — ожидающим вызовам, сигналы — подпискам.
func (c *Conn) readLoop() {
	for {
		m, err := ReadMessage(c.r)
		if err != nil {
			c.fail(fmt.Errorf("dbus: чтение: %w", err))
			return
		}
		switch m.Type {
		case TypeMethodReturn, TypeError:
			c.mu.Lock()
			ch := c.pending[m.ReplySerial]
			delete(c.pending, m.ReplySerial)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case TypeSignal:
			c.mu.Lock()
			var to []*Subscription
			for s := range c.subs {
				if s.match.matches(m) {
					to = append(to, s)
				}
			}
			c.mu.Unlock()
			for _, s := range to {
				s.deliver(m)
			}
		case TypeMethodCall:
			// своих объектов на шине нет: вежливо отказать, чтобы вызывающий не ждал до таймаута
			if m.Flags&FlagNoReplyExpected == 0 {
				go c.send(&Message{Type: TypeError, Destination: m.Sender, ReplySerial: m.Serial,
					ErrorName: "org.freedesktop.DBus.Error.UnknownMethod", Signature: "s", Body: []any{"объектов нет"}})
			}
		}
	}
}

// Match — какие сигналы получать: правило для шины (AddMatch) и тот же фильтр на месте (подписок может быть несколько).
// Пустые поля не ограничивают. Sender — имя сервиса для шины (например «org.bluez»); на месте не проверяется: в сигнале
// стоит уникальное имя отправителя.
type Match struct {
	Sender        string
	Interface     string
	Member        string
	PathNamespace ObjectPath
}

func (m Match) rule() string {
	parts := []string{"type='signal'"}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"='"+v+"'")
		}
	}
	add("sender", m.Sender)
	add("interface", m.Interface)
	add("member", m.Member)
	add("path_namespace", string(m.PathNamespace))
	return strings.Join(parts, ",")
}

func (m Match) matches(msg *Message) bool {
	if m.Interface != "" && msg.Interface != m.Interface || m.Member != "" && msg.Member != m.Member {
		return false
	}
	if ns := string(m.PathNamespace); ns != "" && ns != "/" {
		p := string(msg.Path)
		if p != ns && !strings.HasPrefix(p, ns+"/") {
			return false
		}
	}
	return true
}

// Subscription — подписка на сигналы. C закрывается при Close подписки или соединения. Если читатель не успевает
// (больше 64 сигналов в очереди), лишние сигналы теряются и считаются в Dropped.
type Subscription struct {
	C       <-chan *Message
	c       chan *Message
	conn    *Conn
	match   Match
	once    sync.Once
	mu      sync.Mutex
	dropped int
}

func (s *Subscription) deliver(m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c == nil {
		return
	}
	select {
	case s.c <- m:
	default:
		s.dropped++
	}
}

func (s *Subscription) closeCh() {
	s.once.Do(func() {
		s.mu.Lock()
		close(s.c)
		s.c = nil
		s.mu.Unlock()
	})
}

// Dropped — сколько сигналов потеряно из-за медленного читателя.
func (s *Subscription) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close снимает подписку (RemoveMatch) и закрывает C.
func (s *Subscription) Close() {
	s.conn.mu.Lock()
	_, live := s.conn.subs[s]
	delete(s.conn.subs, s)
	s.conn.mu.Unlock()
	s.closeCh()
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.conn.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RemoveMatch", "s", s.match.rule())
	}
}

// Subscribe просит шину присылать сигналы по m и возвращает подписку.
func (c *Conn) Subscribe(ctx context.Context, m Match) (*Subscription, error) {
	ch := make(chan *Message, 64)
	s := &Subscription{C: ch, c: ch, conn: c, match: m}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.subs[s] = true
	c.mu.Unlock()
	if _, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "AddMatch", "s", m.rule()); err != nil {
		c.mu.Lock()
		delete(c.subs, s)
		c.mu.Unlock()
		s.closeCh()
		return nil, err
	}
	return s, nil
}
