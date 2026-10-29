package btout

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// event — struct input_event, как её отдаёт ядро (время не важно).
func event(typ, code uint16, value int32) []byte {
	b := make([]byte, inputEventSize)
	tail := b[inputEventSize-8:]
	binary.LittleEndian.PutUint16(tail, typ)
	binary.LittleEndian.PutUint16(tail[2:], code)
	binary.LittleEndian.PutUint32(tail[4:], uint32(value))
	return b
}

func TestReadButtons(t *testing.T) {
	var in bytes.Buffer
	for _, e := range [][]byte{
		event(evKey, keyPlayCD, 1), event(0, 0, 0), event(evKey, keyPlayCD, 0), // нажатие, синхронизация, отпускание
		event(evKey, keyPauseCD, 1),                                // сразу следом — дребезг той же кнопки
		event(evKey, keyNextSong, 1), event(evKey, keyNextSong, 2), // повтор при удержании — не нажатие
		event(evKey, keyPrevSong, 1), event(evKey, keyStopCD, 1),
		event(evKey, 115, 1), // громкость — не кнопка плеера
	} {
		in.Write(e)
	}
	var got []Button
	if err := readButtons(&in, func(b Button) { got = append(got, b) }); err != io.EOF {
		t.Fatal(err)
	}
	want := []Button{ButtonPlayPause, ButtonNext, ButtonPrev, ButtonStop}
	if len(got) != len(want) {
		t.Fatalf("кнопки %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("кнопки %v, ждали %v", got, want)
		}
	}
}

// sysfs — каталог устройств ввода, как /sys/class/input на колонке.
func sysfs(t *testing.T, names map[string]string) string {
	dir := t.TempDir()
	for ev, name := range names {
		os.MkdirAll(filepath.Join(dir, ev, "device"), 0o755)
		os.WriteFile(filepath.Join(dir, ev, "device", "name"), []byte(name+"\n"), 0o644)
	}
	os.MkdirAll(filepath.Join(dir, "input10"), 0o755) // не event* — пропускается
	return dir
}

func TestFindButtons(t *testing.T) {
	sys := sysfs(t, map[string]string{"event0": "gpio-keys", "event1": "aw933xx", "event2": "ROG DELTA II (BT) (AVRCP)"})
	if p, err := findButtons(sys, "/dev/input", []string{"ROG DELTA II (BT)", ""}); err != nil || p != filepath.Join("/dev/input", "event2") {
		t.Fatalf("%q %v", p, err)
	}
	// псевдоним другой, но устройство AVRCP одно — оно
	if p, err := findButtons(sys, "/dev/input", []string{"Мои наушники"}); err != nil || p != filepath.Join("/dev/input", "event2") {
		t.Fatalf("единственное: %q %v", p, err)
	}
	two := sysfs(t, map[string]string{"event2": "ROG DELTA II (BT) (AVRCP)", "event3": "FreeBuds (AVRCP)"})
	if p, err := findButtons(two, "/dev/input", []string{"FreeBuds"}); err != nil || p != filepath.Join("/dev/input", "event3") {
		t.Fatalf("по имени из двух: %q %v", p, err)
	}
	if _, err := findButtons(two, "/dev/input", []string{"Другие"}); !errors.Is(err, errNoButtons) {
		t.Fatalf("двух чужих не выбирать: %v", err)
	}
	if _, err := findButtons(sysfs(t, map[string]string{"event0": "gpio-keys"}), "/dev/input", nil); !errors.Is(err, errNoButtons) {
		t.Fatalf("без AVRCP: %v", err)
	}
}

// Кнопки читаются, пока наушники подключены: нажатие доходит до Buttons, отключение закрывает устройство.
func TestButtonsFollowHeadphones(t *testing.T) {
	fc := newFakeClient()
	r, w := io.Pipe()
	var mu sync.Mutex
	var pressed []Button
	var asked []string
	s := &Sink{
		OpenButtons: func(names []string) (io.ReadCloser, error) {
			mu.Lock()
			asked = append(asked, names...)
			mu.Unlock()
			return r, nil
		},
		Buttons: func(b Button) { mu.Lock(); pressed = append(pressed, b); mu.Unlock() },
	}
	start(t, s, fc, nil)
	fc.connect(hp, hpPath())
	waitFor(t, "кнопки читаются", func() bool { return s.DebugState().(State).Buttons })
	if _, err := w.Write(event(evKey, keyNextSong, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "нажатие дошло", func() bool { mu.Lock(); defer mu.Unlock(); return len(pressed) == 1 && pressed[0] == ButtonNext })
	mu.Lock()
	if len(asked) == 0 || asked[0] != "FreeBuds" {
		t.Fatalf("устройство ищется по имени наушников: %v", asked)
	}
	mu.Unlock()
	fc.disconnect()
	waitFor(t, "кнопки закрыты", func() bool { return !s.DebugState().(State).Buttons })
	done := make(chan error, 1)
	go func() { _, err := w.Write(event(evKey, keyNextSong, 1)); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("после отключения устройство кнопок закрыто")
		}
	case <-time.After(time.Second):
		t.Fatal("запись в закрытое устройство повисла")
	}
}
