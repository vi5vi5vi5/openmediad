//go:build linux && (arm64 || amd64)

package alsactl

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// devControl — /dev/snd/controlC<N> в пуле ожидания Go: Read не держит поток, Close прерывает Read.
type devControl struct{ f *os.File }

// openControl открывает устройство и сразу подписывается на события, и только потом отдаёт дескриптор пулу ожидания Go.
// Порядок важен: драйвер (snd_ctl_poll) не встаёт в очередь ожидания у неподписанного файла, а Go регистрирует файл в epoll
// при открытии — открытый через os.OpenFile файл так и не просыпался бы (проверено на колонке 28.10.2026).
func openControl(card int) (control, error) {
	path := fmt.Sprintf("/dev/snd/controlC%d", card)
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	on := int32(1)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlSubscribe, uintptr(unsafe.Pointer(&on))); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("alsactl: подписка на события: %w", errno)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &devControl{f: os.NewFile(uintptr(fd), path)}, nil
}

func (c *devControl) ioctl(req uintptr, arg unsafe.Pointer) error {
	sc, err := c.f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := sc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func (c *devControl) readInt(name string) (int, error) {
	var b [valueSize]byte
	if err := putID(b[:], name); err != nil {
		return 0, err
	}
	if err := c.ioctl(ioctlRead, unsafe.Pointer(&b[0])); err != nil {
		if err == syscall.ENOENT {
			return 0, ErrNoControl
		}
		return 0, fmt.Errorf("alsactl: чтение %q: %w", name, err)
	}
	return int(int64(binary.LittleEndian.Uint64(b[valueDataOff:]))), nil
}

func (c *devControl) info(name string) (min, max, count int, err error) {
	var b [infoSize]byte
	if err := putID(b[:], name); err != nil {
		return 0, 0, 0, err
	}
	if err := c.ioctl(ioctlInfo, unsafe.Pointer(&b[0])); err != nil {
		if err == syscall.ENOENT {
			return 0, 0, 0, ErrNoControl
		}
		return 0, 0, 0, fmt.Errorf("alsactl: сведения о %q: %w", name, err)
	}
	count = int(binary.LittleEndian.Uint32(b[infoCountOff:]))
	if binary.LittleEndian.Uint32(b[infoTypeOff:]) != typeInteger || count == 0 {
		return 0, 0, 0, ErrNoControl
	}
	return int(int64(binary.LittleEndian.Uint64(b[infoMinOff:]))), int(int64(binary.LittleEndian.Uint64(b[infoMaxOff:]))), count, nil
}

// writeInt пишет значение v во все count каналов регулятора (value.integer.value[i] — long).
func (c *devControl) writeInt(name string, v, count int) error {
	var b [valueSize]byte
	if err := putID(b[:], name); err != nil {
		return err
	}
	for i := 0; i < count && valueDataOff+8*(i+1) <= valueSize; i++ {
		binary.LittleEndian.PutUint64(b[valueDataOff+8*i:], uint64(int64(v)))
	}
	if err := c.ioctl(ioctlWrite, unsafe.Pointer(&b[0])); err != nil {
		return fmt.Errorf("alsactl: запись %q: %w", name, err)
	}
	return nil
}

func (c *devControl) subscribe() error { return nil } // подписка — уже при открытии

func (c *devControl) read(p []byte) (int, error) { return c.f.Read(p) }

func (c *devControl) close() error { return c.f.Close() }
