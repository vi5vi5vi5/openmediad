package capture

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Журнал кадров (.omdf) — уже разобранные кадры шины: так обезличенные записи хранятся в testdata/ (pcap после
// обезличивания пересобирать незачем). Формат:
//
//	"OMDF1\n", затем записи: i64 LE время (Unix, нс) · u8 длина имени сервиса · имя · u8 направление (1 — к сервису)
//	· u16 LE порт клиента · 4 байта адреса клиента · u32 LE длина кадра · кадр.
const logMagic = "OMDF1\n"

// LogWriter пишет журнал кадров.
type LogWriter struct {
	w   *bufio.Writer
	err error
}

// NewLogWriter начинает журнал (пишет заголовок).
func NewLogWriter(w io.Writer) *LogWriter {
	lw := &LogWriter{w: bufio.NewWriter(w)}
	_, lw.err = lw.w.WriteString(logMagic)
	return lw
}

// Write добавляет кадр.
func (l *LogWriter) Write(f Frame) error {
	if l.err != nil {
		return l.err
	}
	if len(f.Svc) > 255 {
		return errors.New("capture: слишком длинное имя сервиса")
	}
	var h [8 + 1]byte
	binary.LittleEndian.PutUint64(h[:8], uint64(f.Time.UnixNano()))
	h[8] = byte(len(f.Svc))
	l.w.Write(h[:])
	l.w.WriteString(f.Svc)
	var t [1 + 2 + 4 + 4]byte
	if f.ToSvc {
		t[0] = 1
	}
	binary.LittleEndian.PutUint16(t[1:3], f.Client)
	copy(t[3:7], f.ClientIP[:])
	binary.LittleEndian.PutUint32(t[7:11], uint32(len(f.Data)))
	l.w.Write(t[:])
	_, l.err = l.w.Write(f.Data)
	return l.err
}

// Close дописывает буфер.
func (l *LogWriter) Close() error {
	if l.err != nil {
		return l.err
	}
	return l.w.Flush()
}

// WalkLog читает журнал кадров и отдаёт кадры fn по порядку.
func WalkLog(r io.Reader, fn func(Frame)) error {
	br := bufio.NewReaderSize(r, 256<<10)
	magic := make([]byte, len(logMagic))
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != logMagic {
		return errors.New("capture: это не журнал кадров OMDF1")
	}
	var data []byte
	for {
		var h [9]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("capture: обрезанная запись: %w", err)
		}
		svc := make([]byte, h[8])
		var t [11]byte
		if _, err := io.ReadFull(br, svc); err != nil {
			return err
		}
		if _, err := io.ReadFull(br, t[:]); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(t[7:11])
		if n > 10<<20 {
			return fmt.Errorf("capture: кадр %d Б", n)
		}
		if uint32(cap(data)) < n {
			data = make([]byte, n)
		}
		data = data[:n]
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		f := Frame{
			Time:   time.Unix(0, int64(binary.LittleEndian.Uint64(h[:8]))).UTC(),
			Svc:    string(svc),
			ToSvc:  t[0] == 1,
			Client: binary.LittleEndian.Uint16(t[1:3]),
			Data:   data,
		}
		copy(f.ClientIP[:], t[3:7])
		fn(f)
	}
}

// WalkFile читает запись любого вида: pcap (tcpdump) или журнал кадров — по первым байтам.
func WalkFile(path string, fn func(Frame)) error { return WalkFilePorts(path, Ports, fn) }

// WalkFilePorts — WalkFile со своей таблицей портов для pcap (у журнала кадров сервисы уже названы).
func WalkFilePorts(path string, ports map[uint16]string, fn func(Frame)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(logMagic))
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if bytes.Equal(head[:n], []byte(logMagic)) {
		return WalkLog(f, fn)
	}
	return WalkPcapPorts(f, ports, fn)
}

// Recordings — записи для золотых тестов: *.pcap и *.omdf из каталога OMD_CAPTURES (по умолчанию captures/ в корне
// проекта: там лежат личные записи разработчика, в git они не попадают) и обезличенные *.omdf из testdata/. Второй
// результат — где искали (для сообщения о пропуске теста).
func Recordings() ([]string, string) {
	root := projectRoot()
	dir := os.Getenv("OMD_CAPTURES")
	if dir == "" && root != "" {
		dir = filepath.Join(root, "captures")
	}
	var out []string
	for _, pat := range []string{"*.pcap", "*.omdf"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		out = append(out, m...)
	}
	if root != "" {
		m, _ := filepath.Glob(filepath.Join(root, "testdata", "*.omdf"))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out, dir + " и testdata/"
}

// projectRoot ищет корень проекта (каталог с go.mod) вверх от текущего каталога.
func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
}
