package bluetooth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Кнопки наушников (AVRCP). Свой плеер (MPRIS) колонка в BlueZ не регистрирует, поэтому команды плеера от наушников
// (play, pause, next, previous) BlueZ отдаёт не по D-Bus, а устройству ввода «<имя наушников> (AVRCP)» (uinput,
// /dev/input/eventN). Платформа это устройство не читает — кнопки молчали. Выход читает его сам и отдаёт нажатия в
// Sink.Buttons (оттуда они уходят платформе командами плеера, как кнопки maind --cli). Устройство появляется, когда
// наушники подключили AVRCP (бывает на секунды позже звука), и исчезает при отключении.
// [Проверено 28.10.2026: «ROG DELTA II (BT) (AVRCP)», /dev/input/event2.]

// Button — кнопка наушников.
type Button int

const (
	// ButtonPlayPause — play, pause или play/pause: какую из них пришлют наушники, зависит от того, что они думают о
	// воспроизведении, а колонка им об этом не сообщает; поэтому все три значат «пауза или продолжить».
	ButtonPlayPause Button = iota + 1
	ButtonStop
	ButtonNext
	ButtonPrev
)

func (b Button) String() string {
	switch b {
	case ButtonPlayPause:
		return "пауза/продолжить"
	case ButtonStop:
		return "стоп"
	case ButtonNext:
		return "дальше"
	case ButtonPrev:
		return "назад"
	}
	return fmt.Sprintf("кнопка %d", int(b))
}

// Коды клавиш (linux/input-event-codes.h), в которые BlueZ переводит команды AVRCP (avctp.c, key_map).
const (
	evKey        = 1
	keyNextSong  = 163 // AVRCP FORWARD
	keyPlayPause = 164
	keyPrevSong  = 165 // AVRCP BACKWARD
	keyStopCD    = 166 // AVRCP STOP
	keyPlayCD    = 200 // AVRCP PLAY
	keyPauseCD   = 201 // AVRCP PAUSE
)

func buttonOf(code uint16) (Button, bool) {
	switch code {
	case keyPlayCD, keyPauseCD, keyPlayPause:
		return ButtonPlayPause, true
	case keyStopCD:
		return ButtonStop, true
	case keyNextSong:
		return ButtonNext, true
	case keyPrevSong:
		return ButtonPrev, true
	}
	return 0, false
}

// inputEventSize — struct input_event: struct timeval (два long) + type, code (по 2 байта) + value (4 байта); у
// 64-битного демона — 24 байта.
const inputEventSize = 2*strconv.IntSize/8 + 8

// buttonRepeat — повтор той же кнопки быстрее этого считается дребезгом (наушники иногда шлют play и pause подряд).
const buttonRepeat = 300 * time.Millisecond

// readButtons читает события устройства ввода до ошибки или конца и отдаёт нажатия (не отпускания) кнопок плеера.
func readButtons(r io.Reader, on func(Button)) error {
	buf := make([]byte, inputEventSize)
	var last Button
	var lastAt time.Time
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		ev := buf[inputEventSize-8:]
		typ, code, value := binary.LittleEndian.Uint16(ev), binary.LittleEndian.Uint16(ev[2:]), int32(binary.LittleEndian.Uint32(ev[4:]))
		if typ != evKey || value != 1 {
			continue
		}
		b, ok := buttonOf(code)
		if !ok {
			continue
		}
		now := time.Now()
		if b == last && now.Sub(lastAt) < buttonRepeat {
			continue
		}
		last, lastAt = b, now
		on(b)
	}
}

// errNoButtons — устройства кнопок наушников пока нет (AVRCP ещё не подключился или наушники его не поддерживают).
var errNoButtons = errors.New("устройства ввода AVRCP нет")

// findButtons ищет устройство ввода BlueZ для наушников с одним из имён names: «<имя> (AVRCP)». sys — каталог устройств
// ввода в sysfs (/sys/class/input), dev — их узлов (/dev/input). Имени нет, но устройство AVRCP одно — оно (имя могло
// смениться псевдонимом).
func findButtons(sys, dev string, names []string) (string, error) {
	entries, err := os.ReadDir(sys)
	if err != nil {
		return "", err
	}
	var avrcp []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sys, e.Name(), "device", "name"))
		if err != nil {
			continue
		}
		base, ok := strings.CutSuffix(strings.TrimSpace(string(b)), " (AVRCP)")
		if !ok {
			continue
		}
		for _, n := range names {
			if n != "" && n == base {
				return filepath.Join(dev, e.Name()), nil
			}
		}
		avrcp = append(avrcp, filepath.Join(dev, e.Name()))
	}
	if len(avrcp) == 1 {
		return avrcp[0], nil
	}
	return "", errNoButtons
}

// ensureButtons начинает читать кнопки наушников t, если ещё не читает. false — устройства пока нет (повторить позже).
func (s *Sink) ensureButtons(t *target) bool {
	if s.Buttons == nil || t == nil {
		return true
	}
	s.mu.Lock()
	if s.keys != nil && s.keysAddr == t.addr {
		s.mu.Unlock()
		return true
	}
	old := s.keys
	s.keys = nil
	s.keysTries++
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	open := s.OpenButtons
	if open == nil {
		open = openButtons
	}
	r, err := open(t.names)
	if err != nil {
		if !errors.Is(err, errNoButtons) {
			s.note("кнопки наушников: %v", err)
		}
		return false
	}
	s.mu.Lock()
	s.keys, s.keysAddr = r, t.addr
	s.mu.Unlock()
	s.logf("кнопки наушников %s подключены: пауза, дальше и назад уходят платформе", t.label())
	go func() {
		readButtons(r, s.Buttons) // кончается, когда устройство пропало или closeButtons его закрыл
		r.Close()
		s.mu.Lock()
		if s.keys == r {
			s.keys = nil // пропало само: serve попробует открыть снова
		}
		s.mu.Unlock()
	}()
	return true
}

// buttonsWait — через сколько снова искать устройство кнопок: первые полминуты после подключения часто (AVRCP
// подключается позже звука), потом редко (у наушников может не быть AVRCP вовсе). 0 — искать не нужно.
func (s *Sink) buttonsWait() time.Duration {
	if s.Buttons == nil || s.cur.Load() == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.keys != nil:
		return 0
	case s.keysTries < 15:
		return 2 * time.Second
	}
	return 30 * time.Second
}

// closeButtons перестаёт читать кнопки (наушники отключились или выход останавливается).
func (s *Sink) closeButtons() {
	s.mu.Lock()
	r := s.keys
	s.keys, s.keysAddr, s.keysTries = nil, "", 0
	s.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// openButtons — устройство кнопок наушников на колонке.
func openButtons(names []string) (io.ReadCloser, error) {
	p, err := findButtons("/sys/class/input", "/dev/input", names)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
