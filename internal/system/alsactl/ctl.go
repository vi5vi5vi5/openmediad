package alsactl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Регулятор громкости через управляющее устройство ALSA (/dev/snd/controlC0): чтение значения и подписка на изменения
// без amixer и без опроса. Нужен выходам, на которые аппаратный Master не действует (Bluetooth-наушники): громкостью колонки
// по-прежнему управляют кнопки и Алиса, а движок повторяет её программно (PLAN.md, §11.4, R14). ioctl идёт через ядро, а не
// через общую память, поэтому 64-битный процесс безопасен для штатного звука (в отличие от смесителя dmix).
//
// Раскладка структур ядра (include/uapi/sound/asound.h) — для 64-битного Linux: колонка (arm64) и компьютер (amd64).

// Смещения и размеры структур snd_ctl_* на 64-битном ядре.
const (
	idSize       = 64   // snd_ctl_elem_id: numid, iface, device, subdevice, name[44], index
	idNameOff    = 16   // name[44]
	idNameLen    = 44   // с завершающим нулём
	valueSize    = 1224 // snd_ctl_elem_value: id, indirect, (выравнивание), value.integer[128] (long), tstamp, reserved
	valueDataOff = 72   // value.integer.value[0]
	infoSize     = 272  // snd_ctl_elem_info
	infoTypeOff  = 64
	infoCountOff = 72
	infoMinOff   = 80 // value.integer.min (long)
	infoMaxOff   = 88
	eventSize    = 72 // snd_ctl_event: type, data.elem{mask, id}
	eventMaskOff = 4
	eventIDOff   = 8
	ifaceMixer   = 2
	typeInteger  = 2
	eventElem    = 0
	maskValue    = 1
	maskRemove   = ^uint32(0)
)

// Номера ioctl: _IOWR('U', номер, размер структуры).
const (
	ioctlRead      uintptr = 0xC0000000 | valueSize<<16 | 'U'<<8 | 0x12 // SNDRV_CTL_IOCTL_ELEM_READ
	ioctlWrite     uintptr = 0xC0000000 | valueSize<<16 | 'U'<<8 | 0x13 // SNDRV_CTL_IOCTL_ELEM_WRITE
	ioctlInfo      uintptr = 0xC0000000 | infoSize<<16 | 'U'<<8 | 0x11  // SNDRV_CTL_IOCTL_ELEM_INFO
	ioctlSubscribe uintptr = 0xC0000000 | 4<<16 | 'U'<<8 | 0x16         // SNDRV_CTL_IOCTL_SUBSCRIBE_EVENTS
)

// ErrNoControl — регулятора с таким именем нет, или это не целое число.
var ErrNoControl = errors.New("alsactl: нет такого регулятора")

// putID кладёт идентификатор «регулятор микшера по имени» (numid = 0: ядро ищет по имени и номеру).
func putID(b []byte, name string) error {
	if len(name) >= idNameLen {
		return errors.New("alsactl: слишком длинное имя регулятора")
	}
	for i := range b[:idSize] {
		b[i] = 0
	}
	binary.LittleEndian.PutUint32(b[4:], ifaceMixer)
	copy(b[idNameOff:idNameOff+idNameLen], name)
	return nil
}

// idName — имя регулятора из идентификатора.
func idName(b []byte) string {
	n := b[idNameOff : idNameOff+idNameLen]
	for i, c := range n {
		if c == 0 {
			return string(n[:i])
		}
	}
	return string(n)
}

// parseEvents разбирает прочитанные события: имена регуляторов, у которых изменилось значение (или которые удалены).
func parseEvents(b []byte) (names []string) {
	for len(b) >= eventSize {
		ev := b[:eventSize]
		b = b[eventSize:]
		if binary.LittleEndian.Uint32(ev) != eventElem {
			continue
		}
		mask := binary.LittleEndian.Uint32(ev[eventMaskOff:])
		if mask == maskRemove || mask&maskValue != 0 {
			names = append(names, idName(ev[eventIDOff:eventIDOff+idSize]))
		}
	}
	return names
}

// Watch — целочисленный регулятор (громкость Master) с подпиской: Level отдаёт значение без системных вызовов, Changes —
// отметку о каждом изменении (несколько подряд сливаются).
type Watch struct {
	name    string
	min     int
	max     int
	count   int // каналов у регулятора (Set пишет во все)
	cur     atomic.Int64
	changes chan struct{}
	ctl     control
	once    sync.Once
	done    chan struct{}
	err     error // почему слежение прекратилось (после done)
}

// control — открытое управляющее устройство (на Linux — файл; в тестах — подделка).
type control interface {
	readInt(name string) (int, error)
	writeInt(name string, v, count int) error
	info(name string) (min, max, count int, err error)
	subscribe() error
	read(p []byte) (int, error)
	close() error
}

// WatchInteger открывает управляющее устройство карты 0 и следит за регулятором name (например «Master»).
func WatchInteger(name string) (*Watch, error) {
	c, err := openControl(0)
	if err != nil {
		return nil, err
	}
	return newWatch(c, name)
}

func newWatch(c control, name string) (*Watch, error) {
	min, max, count, err := c.info(name)
	if err != nil {
		c.close()
		return nil, err
	}
	v, err := c.readInt(name)
	if err != nil {
		c.close()
		return nil, err
	}
	if err := c.subscribe(); err != nil {
		c.close()
		return nil, err
	}
	w := &Watch{name: name, min: min, max: max, count: count, changes: make(chan struct{}, 1), ctl: c, done: make(chan struct{})}
	w.cur.Store(int64(v))
	go w.loop()
	return w, nil
}

func (w *Watch) loop() {
	defer close(w.done)
	buf := make([]byte, eventSize*16)
	for {
		n, err := w.ctl.read(buf)
		if err != nil {
			w.err = err
			return
		}
		for _, name := range parseEvents(buf[:n]) {
			if name != w.name {
				continue
			}
			v, err := w.ctl.readInt(w.name)
			if err != nil || int64(v) == w.cur.Load() {
				continue
			}
			w.cur.Store(int64(v))
			select {
			case w.changes <- struct{}{}:
			default:
			}
		}
	}
}

// Level — текущее значение относительно минимума и размах (cur из max); ok = false после Close.
func (w *Watch) Level() (cur, max int, ok bool) {
	select {
	case <-w.done:
		return 0, 0, false
	default:
	}
	return int(w.cur.Load()) - w.min, w.max - w.min, true
}

// Err — почему слежение прекратилось (nil, пока идёт).
func (w *Watch) Err() error {
	select {
	case <-w.done:
		return w.err
	default:
		return nil
	}
}

// Changes — канал отметок об изменении значения.
func (w *Watch) Changes() <-chan struct{} { return w.changes }

// Set ставит значение (относительно минимума, как Level; всем каналам регулятора). Изменение придёт и в Changes.
func (w *Watch) Set(v int) error {
	if v < 0 || v > w.max-w.min {
		return fmt.Errorf("alsactl: %q: значение %d вне 0…%d", w.name, v, w.max-w.min)
	}
	return w.ctl.writeInt(w.name, v+w.min, w.count)
}

// Close закрывает устройство; слежение прекращается.
func (w *Watch) Close() {
	w.once.Do(func() {
		w.ctl.close()
		<-w.done
	})
}

// ReadInteger — разовое чтение регулятора карты 0: значение и пределы (для команд CLI).
func ReadInteger(name string) (val, min, max int, err error) {
	c, err := openControl(0)
	if err != nil {
		return 0, 0, 0, err
	}
	defer c.close()
	if min, max, _, err = c.info(name); err != nil {
		return 0, 0, 0, err
	}
	val, err = c.readInt(name)
	return val, min, max, err
}
