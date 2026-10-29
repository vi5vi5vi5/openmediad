package journal

import (
	"os"
	"path/filepath"
	"sync"
)

// FileWriter пишет строки журнала в файл с пределом размера: когда файл дорастает до Max байт, он переименовывается в
// «.1» (прежний «.1» удаляется) и начинается новый. На колонке это флеш: писать в файл стоит только при разборе проблем.
type FileWriter struct {
	Path string
	Max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

func (w *FileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		st, _ := f.Stat()
		w.f = f
		if st != nil {
			w.size = st.Size()
		}
	}
	if w.Max > 0 && w.size+int64(len(p)) > w.Max {
		w.f.Close()
		os.Rename(w.Path, w.Path+".1")
		f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			w.f = nil
			return 0, err
		}
		w.f, w.size = f, 0
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close закрывает файл.
func (w *FileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
