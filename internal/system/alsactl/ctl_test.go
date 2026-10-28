package alsactl

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPutIDLayout(t *testing.T) {
	b := make([]byte, idSize)
	for i := range b {
		b[i] = 0xff // мусор должен быть стёрт
	}
	if err := putID(b, "Master"); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(b[0:]) != 0 || binary.LittleEndian.Uint32(b[4:]) != ifaceMixer || binary.LittleEndian.Uint32(b[60:]) != 0 {
		t.Fatalf("numid/iface/index: % x", b)
	}
	if idName(b) != "Master" || b[idNameOff+6] != 0 {
		t.Fatalf("имя: %q", idName(b))
	}
	if err := putID(b, string(make([]byte, idNameLen))); err == nil {
		t.Fatal("имя без места под завершающий ноль должно отвергаться")
	}
}

// Размеры и номера ioctl — из asound.h для 64-битного ядра; ошибка в них ломает чтение громкости на колонке.
func TestIoctlNumbers(t *testing.T) {
	if ioctlRead != 0xC4C85512 || ioctlInfo != 0xC1105511 || ioctlSubscribe != 0xC0045516 {
		t.Fatalf("ioctl: %#x %#x %#x", ioctlRead, ioctlInfo, ioctlSubscribe)
	}
}

func event(typ, mask uint32, name string) []byte {
	b := make([]byte, eventSize)
	binary.LittleEndian.PutUint32(b, typ)
	binary.LittleEndian.PutUint32(b[eventMaskOff:], mask)
	putID(b[eventIDOff:eventIDOff+idSize], name)
	return b
}

func TestParseEvents(t *testing.T) {
	var b []byte
	b = append(b, event(eventElem, maskValue, "Master")...)
	b = append(b, event(eventElem, 4, "Bluetooth Master")...) // только сведения (INFO), не значение
	b = append(b, event(1, maskValue, "Other")...)            // не событие регулятора
	b = append(b, event(eventElem, maskValue|4, "Loopback Enable")...)
	b = append(b, 1, 2, 3) // обрывок
	got := parseEvents(b)
	if len(got) != 2 || got[0] != "Master" || got[1] != "Loopback Enable" {
		t.Fatalf("события: %v", got)
	}
}

// fakeControl — управляющее устройство в памяти: значения регуляторов и очередь событий.
type fakeControl struct {
	mu     sync.Mutex
	vals   map[string]int
	events chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeControl() *fakeControl {
	return &fakeControl{vals: map[string]int{"Master": 46}, events: make(chan []byte, 8), closed: make(chan struct{})}
}

func (f *fakeControl) set(name string, v int) {
	f.mu.Lock()
	f.vals[name] = v
	f.mu.Unlock()
	f.events <- event(eventElem, maskValue, name)
}

func (f *fakeControl) readInt(name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vals[name]
	if !ok {
		return 0, ErrNoControl
	}
	return v, nil
}
func (f *fakeControl) info(name string) (int, int, error) {
	if _, err := f.readInt(name); err != nil {
		return 0, 0, err
	}
	return 0, 255, nil
}
func (f *fakeControl) subscribe() error { return nil }
func (f *fakeControl) read(p []byte) (int, error) {
	select {
	case e := <-f.events:
		return copy(p, e), nil
	case <-f.closed:
		return 0, errors.New("закрыто")
	}
}
func (f *fakeControl) close() error { f.once.Do(func() { close(f.closed) }); return nil }

func TestWatchFollowsChanges(t *testing.T) {
	fc := newFakeControl()
	fc.vals["Loopback Enable"] = 1
	w, err := newWatch(fc, "Master")
	if err != nil {
		t.Fatal(err)
	}
	if cur, max, ok := w.Level(); cur != 46 || max != 255 || !ok {
		t.Fatalf("Level = %d, %d, %v", cur, max, ok)
	}
	fc.set("Loopback Enable", 0) // чужой регулятор: отметки нет
	fc.set("Master", 68)
	select {
	case <-w.Changes():
	case <-time.After(time.Second):
		t.Fatal("нет отметки об изменении")
	}
	if cur, _, _ := w.Level(); cur != 68 {
		t.Fatalf("Level после изменения = %d", cur)
	}
	select {
	case <-w.Changes():
		t.Fatal("лишняя отметка (чужой регулятор или повтор)")
	case <-time.After(50 * time.Millisecond):
	}
	w.Close()
	if _, _, ok := w.Level(); ok {
		t.Fatal("после Close Level не готов")
	}
}

func TestWatchUnknownControl(t *testing.T) {
	if _, err := newWatch(newFakeControl(), "Нет такого"); !errors.Is(err, ErrNoControl) {
		t.Fatalf("ждали ErrNoControl: %v", err)
	}
}
