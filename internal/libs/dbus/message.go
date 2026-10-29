// Package dbus — маленький клиент системной шины D-Bus: вызов методов и приём сигналов. Нужен, чтобы управлять
// Bluetooth так же, как это делает платформа: BlueZ (org.bluez) и BlueALSA (org.bluealsa) живут на системной шине
// колонки (/run/dbus/system_bus_socket, вход только EXTERNAL; docs/BUS-PROTOCOL.md, §2.7а).
//
// Своя реализация вместо библиотеки: нужна малая часть протокола (вызовы, ответы, сигналы, без передачи файлов и без
// своих объектов на шине), а зависимости проект добавляет только осознанно (PLAN.md, §4). Формат — спецификация D-Bus,
// раздел «Message Protocol»: сообщение — заголовок с полями `a(yv)`, выравнивание каждого значения по его типу.
//
// Значения при разборе: y — byte, b — bool, n — int16, q — uint16, i — int32, u — uint32, x — int64, t — uint64,
// d — float64, s и g — string, o — ObjectPath, h — uint32 (номер файла; сами файлы не передаются), v — Variant,
// массив — []any, словарь a{…} — map[any]any, структура — []any.
package dbus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// ObjectPath — путь объекта на шине (тип o).
type ObjectPath string

// Variant — значение с собственной подписью (тип v).
type Variant struct {
	Sig   string
	Value any
}

// Виды сообщений.
const (
	TypeMethodCall   = 1
	TypeMethodReturn = 2
	TypeError        = 3
	TypeSignal       = 4
)

// Флаги сообщения.
const (
	FlagNoReplyExpected = 1
	FlagNoAutoStart     = 2
)

// Коды полей заголовка.
const (
	fieldPath        = 1
	fieldInterface   = 2
	fieldMember      = 3
	fieldErrorName   = 4
	fieldReplySerial = 5
	fieldDestination = 6
	fieldSender      = 7
	fieldSignature   = 8
	fieldUnixFDs     = 9
)

// maxMessage — предел размера сообщения. Спецификация разрешает 128 МиБ; от BlueZ и BlueALSA приходят десятки килобайт.
const maxMessage = 8 << 20

// Message — сообщение шины.
type Message struct {
	Type        byte
	Flags       byte
	Serial      uint32
	Path        ObjectPath
	Interface   string
	Member      string
	ErrorName   string
	ReplySerial uint32
	Destination string
	Sender      string
	Signature   string // подпись тела
	Body        []any
}

// --- кодирование ---

type encoder struct{ b []byte }

func (e *encoder) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) u32(v uint32) { e.align(4); e.b = binary.LittleEndian.AppendUint32(e.b, v) }

func (e *encoder) str(s string) { e.u32(uint32(len(s))); e.b = append(e.b, s...); e.b = append(e.b, 0) }

// value кодирует одно значение полного типа t.
func (e *encoder) value(t string, v any) error {
	bad := func() error {
		return fmt.Errorf("dbus: значение %T не подходит к типу %q", v, t)
	}
	switch t[0] {
	case 'y':
		x, ok := v.(byte)
		if !ok {
			return bad()
		}
		e.b = append(e.b, x)
	case 'b':
		x, ok := v.(bool)
		if !ok {
			return bad()
		}
		n := uint32(0)
		if x {
			n = 1
		}
		e.u32(n)
	case 'n', 'q':
		var n uint16
		switch x := v.(type) {
		case int16:
			n = uint16(x)
		case uint16:
			n = x
		default:
			return bad()
		}
		e.align(2)
		e.b = binary.LittleEndian.AppendUint16(e.b, n)
	case 'i', 'u', 'h':
		var n uint32
		switch x := v.(type) {
		case int32:
			n = uint32(x)
		case uint32:
			n = x
		default:
			return bad()
		}
		e.u32(n)
	case 'x', 't', 'd':
		var n uint64
		switch x := v.(type) {
		case int64:
			n = uint64(x)
		case uint64:
			n = x
		case float64:
			n = math.Float64bits(x)
		default:
			return bad()
		}
		e.align(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, n)
	case 's':
		x, ok := v.(string)
		if !ok {
			return bad()
		}
		e.str(x)
	case 'o':
		switch x := v.(type) {
		case ObjectPath:
			e.str(string(x))
		case string:
			e.str(x)
		default:
			return bad()
		}
	case 'g':
		x, ok := v.(string)
		if !ok || len(x) > 255 {
			return bad()
		}
		e.b = append(e.b, byte(len(x)))
		e.b = append(e.b, x...)
		e.b = append(e.b, 0)
	case 'v':
		x, ok := v.(Variant)
		if !ok {
			return bad()
		}
		if err := checkSingle(x.Sig); err != nil {
			return err
		}
		if err := e.value("g", x.Sig); err != nil {
			return err
		}
		return e.value(x.Sig, x.Value)
	case '(':
		fields, ok := v.([]any)
		if !ok {
			return bad()
		}
		e.align(8)
		inner := t[1 : len(t)-1]
		for _, f := range fields {
			ft, rest, err := nextType(inner)
			if err != nil {
				return err
			}
			if err := e.value(ft, f); err != nil {
				return err
			}
			inner = rest
		}
		if inner != "" {
			return fmt.Errorf("dbus: структуре %q не хватает полей", t)
		}
	case 'a':
		return e.array(t[1:], v)
	default:
		return fmt.Errorf("dbus: тип %q не поддерживается", t)
	}
	return nil
}

// array кодирует массив с типом элемента et. Длина — в байтах, без выравнивания перед первым элементом.
func (e *encoder) array(et string, v any) error {
	e.u32(0)
	at := len(e.b) - 4
	e.align(alignOf(et[0]))
	start := len(e.b)
	if et[0] == '{' {
		kt, rest, err := nextType(et[1 : len(et)-1])
		if err != nil {
			return err
		}
		vt := rest
		entries, err := dictEntries(v)
		if err != nil {
			return fmt.Errorf("dbus: словарь %q: %w", et, err)
		}
		for _, kv := range entries {
			e.align(8)
			if err := e.value(kt, kv[0]); err != nil {
				return err
			}
			if err := e.value(vt, kv[1]); err != nil {
				return err
			}
		}
	} else {
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("dbus: массив %q: нужен []any, а не %T", et, v)
		}
		for _, it := range items {
			if err := e.value(et, it); err != nil {
				return err
			}
		}
	}
	binary.LittleEndian.PutUint32(e.b[at:], uint32(len(e.b)-start))
	return nil
}

// dictEntries раскладывает словарь в пары (ключи по порядку — чтобы сообщение не зависело от обхода карты).
func dictEntries(v any) ([][2]any, error) {
	var out [][2]any
	switch m := v.(type) {
	case map[string]Variant:
		for k, x := range m {
			out = append(out, [2]any{k, x})
		}
	case map[string]any:
		for k, x := range m {
			out = append(out, [2]any{k, x})
		}
	case map[any]any:
		for k, x := range m {
			out = append(out, [2]any{k, x})
		}
	default:
		return nil, fmt.Errorf("нужна карта, а не %T", v)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i][0]) < fmt.Sprint(out[j][0]) })
	return out, nil
}

// encodeBody кодирует значения по подписи sig.
func encodeBody(sig string, args []any) ([]byte, error) {
	e := &encoder{}
	rest := sig
	for i, a := range args {
		if rest == "" {
			return nil, fmt.Errorf("dbus: аргументов больше, чем в подписи %q", sig)
		}
		t, r, err := nextType(rest)
		if err != nil {
			return nil, err
		}
		if err := e.value(t, a); err != nil {
			return nil, fmt.Errorf("dbus: аргумент %d: %w", i+1, err)
		}
		rest = r
	}
	if rest != "" {
		return nil, fmt.Errorf("dbus: аргументов меньше, чем в подписи %q", sig)
	}
	return e.b, nil
}

// Marshal собирает сообщение для отправки (порядок байт — младшим вперёд).
func (m *Message) Marshal() ([]byte, error) {
	body, err := encodeBody(m.Signature, m.Body)
	if err != nil {
		return nil, err
	}
	var fields []any
	add := func(code byte, sig string, v any) { fields = append(fields, []any{code, Variant{sig, v}}) }
	if m.Path != "" {
		add(fieldPath, "o", m.Path)
	}
	if m.Interface != "" {
		add(fieldInterface, "s", m.Interface)
	}
	if m.Member != "" {
		add(fieldMember, "s", m.Member)
	}
	if m.ErrorName != "" {
		add(fieldErrorName, "s", m.ErrorName)
	}
	if m.ReplySerial != 0 {
		add(fieldReplySerial, "u", m.ReplySerial)
	}
	if m.Destination != "" {
		add(fieldDestination, "s", m.Destination)
	}
	if m.Signature != "" {
		add(fieldSignature, "g", m.Signature)
	}
	e := &encoder{b: []byte{'l', m.Type, m.Flags, 1, 0, 0, 0, 0, 0, 0, 0, 0}}
	binary.LittleEndian.PutUint32(e.b[4:], uint32(len(body)))
	binary.LittleEndian.PutUint32(e.b[8:], m.Serial)
	if err := e.value("a(yv)", fields); err != nil {
		return nil, err
	}
	e.align(8)
	return append(e.b, body...), nil
}

// --- разбор ---

type decoder struct {
	b     []byte
	pos   int
	order binary.ByteOrder
}

var errShort = errors.New("dbus: сообщение обрывается")

func (d *decoder) align(n int) error {
	p := (d.pos + n - 1) / n * n
	if p > len(d.b) {
		return errShort
	}
	d.pos = p
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.b) {
		return nil, errShort
	}
	s := d.b[d.pos : d.pos+n]
	d.pos += n
	return s, nil
}

func (d *decoder) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	s, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(s), nil
}

func (d *decoder) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	s, err := d.take(int(n) + 1)
	if err != nil {
		return "", err
	}
	return string(s[:n]), nil
}

// value разбирает одно значение полного типа t. depth ограничивает вложенность (спецификация — не глубже 64).
func (d *decoder) value(t string, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("dbus: слишком глубокая вложенность")
	}
	switch t[0] {
	case 'y':
		s, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return s[0], nil
	case 'b':
		n, err := d.u32()
		return n != 0, err
	case 'n', 'q':
		if err := d.align(2); err != nil {
			return nil, err
		}
		s, err := d.take(2)
		if err != nil {
			return nil, err
		}
		if t[0] == 'n' {
			return int16(d.order.Uint16(s)), nil
		}
		return d.order.Uint16(s), nil
	case 'i':
		n, err := d.u32()
		return int32(n), err
	case 'u', 'h':
		return d.u32()
	case 'x', 't', 'd':
		if err := d.align(8); err != nil {
			return nil, err
		}
		s, err := d.take(8)
		if err != nil {
			return nil, err
		}
		n := d.order.Uint64(s)
		switch t[0] {
		case 'x':
			return int64(n), nil
		case 'd':
			return math.Float64frombits(n), nil
		}
		return n, nil
	case 's':
		return d.str()
	case 'o':
		s, err := d.str()
		return ObjectPath(s), err
	case 'g':
		n, err := d.take(1)
		if err != nil {
			return nil, err
		}
		s, err := d.take(int(n[0]) + 1)
		if err != nil {
			return nil, err
		}
		return string(s[:n[0]]), nil
	case 'v':
		sig, err := d.value("g", depth+1)
		if err != nil {
			return nil, err
		}
		vs := sig.(string)
		if err := checkSingle(vs); err != nil {
			return nil, err
		}
		x, err := d.value(vs, depth+1)
		return Variant{Sig: vs, Value: x}, err
	case '(':
		if err := d.align(8); err != nil {
			return nil, err
		}
		var out []any
		inner := t[1 : len(t)-1]
		for inner != "" {
			ft, rest, err := nextType(inner)
			if err != nil {
				return nil, err
			}
			x, err := d.value(ft, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
			inner = rest
		}
		return out, nil
	case 'a':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		et := t[1:]
		if err := d.align(alignOf(et[0])); err != nil {
			return nil, err
		}
		end := d.pos + int(n)
		if end > len(d.b) {
			return nil, errShort
		}
		if et[0] == '{' {
			kt, vt, err := nextType(et[1 : len(et)-1])
			if err != nil {
				return nil, err
			}
			m := map[any]any{}
			for d.pos < end {
				if err := d.align(8); err != nil {
					return nil, err
				}
				k, err := d.value(kt, depth+1)
				if err != nil {
					return nil, err
				}
				x, err := d.value(vt, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = x
			}
			return m, nil
		}
		out := []any{}
		for d.pos < end {
			x, err := d.value(et, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	}
	return nil, fmt.Errorf("dbus: тип %q не поддерживается", t)
}

// ReadMessage читает одно сообщение из r.
func ReadMessage(r io.Reader) (*Message, error) {
	var head [16]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch head[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("dbus: неизвестный порядок байт %q", head[0])
	}
	bodyLen := order.Uint32(head[4:])
	fieldsLen := order.Uint32(head[12:])
	headLen := (16 + int(fieldsLen) + 7) / 8 * 8
	total := headLen + int(bodyLen)
	if fieldsLen > maxMessage || bodyLen > maxMessage || total > maxMessage {
		return nil, fmt.Errorf("dbus: сообщение %d байт — больше предела", total)
	}
	buf := make([]byte, total)
	copy(buf, head[:])
	if _, err := io.ReadFull(r, buf[16:]); err != nil {
		return nil, err
	}
	m := &Message{Type: head[1], Flags: head[2], Serial: order.Uint32(head[8:])}
	d := &decoder{b: buf[:16+fieldsLen], pos: 12, order: order}
	fv, err := d.value("a(yv)", 0)
	if err != nil {
		return nil, fmt.Errorf("dbus: заголовок: %w", err)
	}
	for _, f := range fv.([]any) {
		st := f.([]any)
		code := st[0].(byte)
		val := st[1].(Variant).Value
		switch code {
		case fieldPath:
			m.Path, _ = val.(ObjectPath)
		case fieldInterface:
			m.Interface, _ = val.(string)
		case fieldMember:
			m.Member, _ = val.(string)
		case fieldErrorName:
			m.ErrorName, _ = val.(string)
		case fieldReplySerial:
			m.ReplySerial, _ = val.(uint32)
		case fieldDestination:
			m.Destination, _ = val.(string)
		case fieldSender:
			m.Sender, _ = val.(string)
		case fieldSignature:
			m.Signature, _ = val.(string)
		}
	}
	bd := &decoder{b: buf[headLen:], order: order}
	rest := m.Signature
	for rest != "" {
		t, r, err := nextType(rest)
		if err != nil {
			return nil, err
		}
		x, err := bd.value(t, 0)
		if err != nil {
			return nil, fmt.Errorf("dbus: тело %s.%s: %w", m.Interface, m.Member, err)
		}
		m.Body = append(m.Body, x)
		rest = r
	}
	return m, nil
}

// --- подписи ---

// nextType отделяет первый полный тип подписи: «a{sv}s» → «a{sv}», «s».
func nextType(sig string) (t, rest string, err error) {
	if sig == "" {
		return "", "", errors.New("dbus: пустая подпись")
	}
	switch sig[0] {
	case 'y', 'b', 'n', 'q', 'i', 'u', 'x', 't', 'd', 's', 'o', 'g', 'v', 'h':
		return sig[:1], sig[1:], nil
	case 'a':
		et, rest, err := nextType(sig[1:])
		if err != nil {
			return "", "", err
		}
		return "a" + et, rest, nil
	case '(', '{':
		closer := byte(')')
		if sig[0] == '{' {
			closer = '}'
		}
		inner := sig[1:]
		n := 0
		for inner != "" && inner[0] != closer {
			_, r, err := nextType(inner)
			if err != nil {
				return "", "", err
			}
			inner = r
			n++
		}
		if inner == "" || n == 0 || (sig[0] == '{' && n != 2) {
			return "", "", fmt.Errorf("dbus: неверная подпись %q", sig)
		}
		l := len(sig) - len(inner) + 1
		return sig[:l], sig[l:], nil
	}
	return "", "", fmt.Errorf("dbus: неверная подпись %q", sig)
}

// checkSingle — подпись варианта: ровно один полный тип.
func checkSingle(sig string) error {
	_, rest, err := nextType(sig)
	if err == nil && rest != "" {
		err = fmt.Errorf("dbus: в варианте больше одного типа: %q", sig)
	}
	return err
}

func alignOf(c byte) int {
	switch c {
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 's', 'o', 'a', 'h':
		return 4
	case 'x', 't', 'd', '(', '{':
		return 8
	}
	return 1
}
