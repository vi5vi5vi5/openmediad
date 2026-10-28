package dbus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNextType(t *testing.T) {
	cases := []struct{ sig, first, rest string }{
		{"s", "s", ""},
		{"ssv", "s", "sv"},
		{"a{sv}s", "a{sv}", "s"},
		{"a{oa{sa{sv}}}", "a{oa{sa{sv}}}", ""},
		{"(yv)u", "(yv)", "u"},
		{"aay", "aay", ""},
	}
	for _, c := range cases {
		f, r, err := nextType(c.sig)
		if err != nil || f != c.first || r != c.rest {
			t.Errorf("nextType(%q) = %q, %q, %v", c.sig, f, r, err)
		}
	}
	for _, bad := range []string{"", "a", "(", "()", "{s}", "{sss}", "z", "(s"} {
		if _, _, err := nextType(bad); err == nil {
			t.Errorf("nextType(%q): ждали ошибку", bad)
		}
	}
}

// Сообщение с вложенными словарями и вариантами (как ответ GetManagedObjects у BlueZ) переживает кодирование и разбор.
func TestRoundTripManagedObjects(t *testing.T) {
	objs := map[any]any{
		ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF"): map[any]any{
			"org.bluez.Device1": map[any]any{
				"Address":   Variant{"s", "AA:BB:CC:DD:EE:FF"},
				"Connected": Variant{"b", true},
				"Class":     Variant{"u", uint32(0x240404)},
				"RSSI":      Variant{"n", int16(-60)},
				"UUIDs":     Variant{"as", []any{"0000110b-0000-1000-8000-00805f9b34fb"}},
			},
		},
		ObjectPath("/org/bluez/hci0"): map[any]any{"org.bluez.Adapter1": map[any]any{"Powered": Variant{"b", true}}},
	}
	m := &Message{Type: TypeMethodReturn, Serial: 7, ReplySerial: 3, Destination: ":1.5", Signature: "a{oa{sa{sv}}}", Body: []any{objs}}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeMethodReturn || got.Serial != 7 || got.ReplySerial != 3 || got.Destination != ":1.5" || got.Signature != m.Signature {
		t.Fatalf("заголовок: %+v", got)
	}
	if !reflect.DeepEqual(got.Body[0], objs) {
		t.Fatalf("тело:\n%#v\nждали\n%#v", got.Body[0], objs)
	}
}

func TestRoundTripScalarsAndStructs(t *testing.T) {
	args := []any{byte(7), true, int16(-2), uint16(3341), int32(-5), uint32(9), int64(-1 << 40), uint64(1 << 50), 2.5,
		"строка", ObjectPath("/a/b"), "a{sv}", Variant{"q", uint16(13)}, []any{[]any{byte(1), "x"}, []any{byte(2), "y"}},
		[]any{}, map[string]Variant{}}
	sig := "ybnqiuxtdsogv" + "a(ys)" + "ay" + "a{sv}"
	m := &Message{Type: TypeSignal, Path: "/x", Interface: "i.f", Member: "M", Signature: sig, Body: args}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]any(nil), args...)
	want[15] = map[any]any{} // словарь при разборе — всегда map[any]any
	if !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("тело:\n%#v\nждали\n%#v", got.Body, want)
	}
	if got.Path != "/x" || got.Interface != "i.f" || got.Member != "M" {
		t.Fatalf("заголовок: %+v", got)
	}
}

// Раскладка байт по спецификации: заголовок 12 байт, длина полей на смещении 12, поля с 16, тело с границы 8 байт;
// пустой массив восьмибайтовых элементов всё равно выравнивается.
func TestWireLayout(t *testing.T) {
	m := &Message{Type: TypeMethodCall, Serial: 1, Path: "/", Member: "Hello", Destination: "d", Signature: "a{sv}u",
		Body: []any{map[string]Variant{}, uint32(0xAABBCCDD)}}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:4]) != "l\x01\x00\x01" || binary.LittleEndian.Uint32(b[8:]) != 1 {
		t.Fatalf("начало заголовка: % x", b[:12])
	}
	fields := binary.LittleEndian.Uint32(b[12:])
	body := (16 + int(fields) + 7) / 8 * 8
	if int(binary.LittleEndian.Uint32(b[4:])) != len(b)-body {
		t.Fatalf("длина тела %d, а после заголовка %d байт", binary.LittleEndian.Uint32(b[4:]), len(b)-body)
	}
	// тело: длина массива 0, выравнивание до 8, затем u32
	want := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0xDD, 0xCC, 0xBB, 0xAA}
	if !bytes.Equal(b[body:], want) {
		t.Fatalf("тело % x, ждали % x", b[body:], want)
	}
}

func TestMarshalRejectsWrongValues(t *testing.T) {
	for _, c := range []struct {
		sig  string
		args []any
	}{
		{"s", []any{1}}, {"u", []any{"x"}}, {"ss", []any{"a"}}, {"s", []any{"a", "b"}}, {"v", []any{"x"}},
		{"v", []any{Variant{"ss", "a"}}}, {"as", []any{"x"}}, {"a{sv}", []any{[]any{}}},
	} {
		if _, err := (&Message{Type: TypeSignal, Signature: c.sig, Body: c.args}).Marshal(); err == nil {
			t.Errorf("%s %v: ждали ошибку", c.sig, c.args)
		}
	}
}

func TestReadMessageRejectsBroken(t *testing.T) {
	m := &Message{Type: TypeSignal, Path: "/x", Member: "M", Signature: "s", Body: []any{"hello"}}
	b, _ := m.Marshal()
	for n := 0; n < len(b); n += 3 {
		if _, err := ReadMessage(bytes.NewReader(b[:n])); err == nil {
			t.Fatalf("обрезанное до %d байт сообщение разобрано", n)
		}
	}
	huge := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(huge[4:], 1<<30)
	if _, err := ReadMessage(bytes.NewReader(huge)); err == nil {
		t.Fatal("сообщение больше предела разобрано")
	}
}

// --- поддельная шина ---

type fakeBus struct {
	t      *testing.T
	conn   net.Conn
	r      *bufio.Reader
	authed chan string
}

// newFakeBus — шина, которая пускает, отвечает на Hello и дальше отдаёт сообщения клиента в handle.
func newFakeBus(t *testing.T, handle func(b *fakeBus, m *Message)) (*Conn, *fakeBus) {
	server, client := net.Pipe()
	b := &fakeBus{t: t, conn: server, r: bufio.NewReader(server), authed: make(chan string, 1)}
	go func() {
		zero, _ := b.r.ReadByte()
		line, _ := b.r.ReadString('\n')
		if zero != 0 {
			line = "нет нулевого байта"
		}
		b.authed <- line
		io.WriteString(server, "OK 0123456789abcdef\r\n")
		if begin, _ := b.r.ReadString('\n'); begin != "BEGIN\r\n" {
			server.Close()
			return
		}
		for {
			m, err := ReadMessage(b.r)
			if err != nil {
				return
			}
			if m.Member == "Hello" {
				b.reply(m, "s", ":1.77")
				continue
			}
			handle(b, m)
		}
	}()
	c, err := NewConn(client, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); server.Close() })
	return c, b
}

func (b *fakeBus) write(m *Message) {
	data, err := m.Marshal()
	if err != nil {
		b.t.Error(err)
		return
	}
	b.conn.Write(data)
}

func (b *fakeBus) reply(to *Message, sig string, body ...any) {
	b.write(&Message{Type: TypeMethodReturn, ReplySerial: to.Serial, Signature: sig, Body: body})
}

func TestConnAuthHelloAndCall(t *testing.T) {
	c, bus := newFakeBus(t, func(b *fakeBus, m *Message) {
		switch m.Member {
		case "Get":
			b.reply(m, "v", Variant{"q", uint16(3341)})
		case "Fail":
			b.write(&Message{Type: TypeError, ReplySerial: m.Serial, ErrorName: "org.bluez.Error.Failed", Signature: "s", Body: []any{"нет устройства"}})
		}
	})
	if line := <-bus.authed; line != "AUTH EXTERNAL 31303030\r\n" { // «1000» в hex
		t.Fatalf("строка входа %q", line)
	}
	if c.Name() != ":1.77" {
		t.Fatalf("имя на шине %q", c.Name())
	}
	ctx := context.Background()
	body, err := c.Call(ctx, "org.bluealsa", "/p", "org.freedesktop.DBus.Properties", "Get", "ss", "org.bluealsa.PCM1", "Volume")
	if err != nil || len(body) != 1 || body[0] != (Variant{"q", uint16(3341)}) {
		t.Fatalf("Get: %v %v", body, err)
	}
	_, err = c.Call(ctx, "org.bluez", "/p", "i", "Fail", "")
	var de *Error
	if !errors.As(err, &de) || de.Name != "org.bluez.Error.Failed" || de.Message != "нет устройства" {
		t.Fatalf("ошибка сервиса: %v", err)
	}
}

func TestCallTimeoutAndClose(t *testing.T) {
	c, _ := newFakeBus(t, func(*fakeBus, *Message) {}) // не отвечает
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "d", "/", "i", "Silent", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ждали таймаут: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "d", "/", "i", "Silent", "")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("после Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("вызов не вернулся после Close")
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done не закрыт")
	}
}

func TestSubscribeDeliversMatchingSignals(t *testing.T) {
	rules := make(chan string, 4)
	c, bus := newFakeBus(t, func(b *fakeBus, m *Message) {
		if m.Member == "AddMatch" {
			rules <- m.Body[0].(string)
			b.reply(m, "")
		}
	})
	ctx := context.Background()
	s, err := c.Subscribe(ctx, Match{Sender: "org.bluez", Interface: "org.freedesktop.DBus.Properties", Member: "PropertiesChanged", PathNamespace: "/org/bluez"})
	if err != nil {
		t.Fatal(err)
	}
	if r := <-rules; r != "type='signal',sender='org.bluez',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged',path_namespace='/org/bluez'" {
		t.Fatalf("правило %q", r)
	}
	sig := func(path ObjectPath, member string) {
		bus.write(&Message{Type: TypeSignal, Path: path, Interface: "org.freedesktop.DBus.Properties", Member: member, Sender: ":1.2",
			Signature: "sa{sv}as", Body: []any{"org.bluez.Device1", map[string]Variant{"Connected": {"b", true}}, []any{}}})
	}
	sig("/org/bluealsa/x", "PropertiesChanged")       // чужое пространство путей
	sig("/org/bluez/hci0/dev_1", "InterfacesAdded")   // чужой метод
	sig("/org/bluezz", "PropertiesChanged")           // похожий, но не вложенный путь
	sig("/org/bluez/hci0/dev_1", "PropertiesChanged") // наш
	select {
	case m := <-s.C:
		if m.Path != "/org/bluez/hci0/dev_1" || !strings.HasPrefix(m.Body[0].(string), "org.bluez") {
			t.Fatalf("сигнал %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("сигнал не пришёл")
	}
	select {
	case m := <-s.C:
		t.Fatalf("лишний сигнал %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
	c.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("канал подписки должен закрыться вместе с соединением")
	}
}

func TestRejectedAuth(t *testing.T) {
	server, client := net.Pipe()
	go func() {
		r := bufio.NewReader(server)
		r.ReadString('\n')
		io.WriteString(server, "REJECTED EXTERNAL\r\n")
	}()
	if _, err := NewConn(client, 0); err == nil || !strings.Contains(err.Error(), "REJECTED") {
		t.Fatalf("ждали отказ: %v", err)
	}
}
