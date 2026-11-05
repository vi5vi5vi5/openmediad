// Package clockled — дисплей с часами Станции Мини 3: 32 светодиодных сегмента (четыре цифры по семь сегментов, двоеточие, средняя точка, градус)
// и анимации прошивки для него.
//
// Железо. Каждый сегмент — светодиод /sys/class/leds/bergamot:white:clock<имя>/brightness (яркость 0…255). Имена: буква сегмента A…G и номер цифры
// 0…3 (A0 — верх первой цифры слева), затем DH и DL (точки двоеточия), DM (средняя точка) и DEGREE (значок градуса). Сегменты семисегментного
// знака: A — верх, B — верх справа, C — низ справа, D — низ, E — низ слева, F — верх слева, G — середина.
//
// Анимации. Файлы ledpatterns/clock/*.binscled.zst — сжатый zstd поток кадров по 35 байт: флаг (у первого кадра 1 — анимация зациклена, у плеера
// 0), 32 байта яркости сегментов в порядке Names и длительность кадра в миллисекундах (два байта, старший первым). Формат разобран по файлам
// player_pause, player_next_track, player_previous_track и сверен с тем, что показывает дисплей: пауза — две вертикали вокруг двоеточия (B1 C1 и E2 F2),
// следующий трек — вертикальные полосы от центра к правому краю (E2 F2, B2 C2, E3 F3, B3 C3), предыдущий — зеркально к левому.
//
// Прошивка перерисовывает дисплей сама: время при смене минуты, спектр во время музыки, свои анимации. Поэтому Player проверяет перед каждым кадром,
// что на дисплее то, что он записал сам, и уступает, если прошивка нарисовала своё; а по окончании возвращает то, что было до анимации (время).
package clockled

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Segments — сколько всего сегментов у дисплея.
const Segments = 4*7 + 4

// SysfsPath — файл яркости сегмента (вместо %s — имя сегмента).
const SysfsPath = "/sys/class/leds/bergamot:white:clock%s/brightness"

// AnimDir — каталог анимаций дисплея в прошивке Мини 3.
const AnimDir = "/system/vendor/quasar/ledpatterns/clock"

// Names — имена сегментов в порядке кадра: цифры 0…3 слева направо по сегментам A…G, затем DH, DL, DM, DEGREE.
var Names = func() []string {
	s := make([]string, 0, Segments)
	for pos := 0; pos < 4; pos++ {
		for _, part := range "ABCDEFG" {
			s = append(s, fmt.Sprintf("%c%d", part, pos))
		}
	}
	return append(s, "DH", "DL", "DM", "DEGREE")
}()

// Index — номер сегмента в кадре по имени («B1»); false — такого нет.
func Index(name string) (int, bool) {
	for i, n := range Names {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// Frame — яркость каждого сегмента (0…255) в порядке Names.
type Frame [Segments]uint8

// IsZero — ни один сегмент не горит.
func (f Frame) IsZero() bool { return f == Frame{} }

// Scale приглушает кадр до яркости b из 255 (255 — как в прошивке).
func (f Frame) Scale(b uint8) Frame {
	if b == 255 {
		return f
	}
	for i := range f {
		f[i] = uint8(uint16(f[i]) * uint16(b) / 255)
	}
	return f
}

// Lit — имена горящих сегментов (для разбора и журнала).
func (f Frame) Lit() []string {
	var out []string
	for i, v := range f {
		if v != 0 {
			out = append(out, fmt.Sprintf("%s=%02x", Names[i], v))
		}
	}
	return out
}

// Device — дисплей.
type Device interface {
	Read() (Frame, error)
	Write(Frame) error
}

// Sysfs — дисплей через sysfs. Файлы яркости остаются открытыми: sysfs позволяет перечитывать и перезаписывать их с нулевого смещения.
type Sysfs struct {
	files []*os.File
	last  Frame // что на дисплее по последнему чтению или записи
	have  bool
}

// OpenSysfs открывает все сегменты. Нет хотя бы одного — ошибка (это не Мини 3).
func OpenSysfs() (*Sysfs, error) {
	s := &Sysfs{}
	for _, name := range Names {
		f, err := os.OpenFile(fmt.Sprintf(SysfsPath, name), os.O_RDWR, 0)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.files = append(s.files, f)
	}
	return s, nil
}

// Close закрывает файлы.
func (s *Sysfs) Close() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, f.Close())
	}
	s.files = nil
	return errors.Join(errs...)
}

// Read читает, что горит на дисплее сейчас (его мог нарисовать кто угодно).
func (s *Sysfs) Read() (Frame, error) {
	var fr Frame
	var buf [8]byte
	for i, f := range s.files {
		n, err := f.ReadAt(buf[:], 0)
		if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
			return fr, fmt.Errorf("%s: %w", Names[i], err)
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
		if err != nil || v < 0 || v > 255 {
			return fr, fmt.Errorf("%s: яркость %q", Names[i], buf[:n])
		}
		fr[i] = uint8(v)
	}
	s.last, s.have = fr, true
	return fr, nil
}

// Write выставляет яркость тем сегментам, которые отличаются от известного состояния дисплея (последнего чтения или записи); первый раз — всем.
func (s *Sysfs) Write(fr Frame) error {
	for i, f := range s.files {
		if s.have && fr[i] == s.last[i] {
			continue
		}
		if _, err := f.WriteAt([]byte(strconv.Itoa(int(fr[i]))), 0); err != nil {
			s.have = false
			return fmt.Errorf("%s: %w", Names[i], err)
		}
	}
	s.last, s.have = fr, true
	return nil
}
