package audio

import (
	"io"
	"sync"
)

// Ring — ограниченный буфер PCM между декодером и выводом. Запись блокируется, пока буфер полон: так давление
// от медленного вывода доходит до декодера и скачивания, а предзагруженный трек не съедает память.
type Ring struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	limit   int // сколько непрочитанных байт держим; остальное место отдано истории для Rewind
	hist    int // сколько уже прочитанных байт ещё не затёрто новыми
	r, n    int
	closed  bool // писатель закончил: после опустошения чтение вернёт io.EOF
	stopped bool // буфер брошен: писатель и читатель получают ErrStopped
	wake    bool
}

// NewRing создаёт буфер на size непрочитанных байт плюс history байт истории: прочитанное остаётся в буфере, пока его не
// затрёт запись, и Rewind может вернуть до history байт назад (так возвращается звук, пропавший в буферах aplay при паузе).
func NewRing(size, history int) *Ring {
	r := &Ring{buf: make([]byte, size+history), limit: size}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Write копирует p в буфер, блокируясь при нехватке места.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for len(p) > 0 {
		for r.n >= r.limit && !r.stopped {
			r.cond.Wait()
		}
		if r.stopped {
			return total, ErrStopped
		}
		w := (r.r + r.n) % len(r.buf)
		k := min(len(p), r.limit-r.n, len(r.buf)-w)
		copy(r.buf[w:], p[:k])
		r.n += k
		r.hist = min(r.hist, len(r.buf)-r.n) // запись затирает самое старое из прочитанного
		p = p[k:]
		total += k
		r.cond.Broadcast()
	}
	return total, nil
}

// ReadWait читает не больше len(p) байт. Блокируется, пока нет данных; возвращает ErrWake после Wake,
// io.EOF, когда писатель закончил и буфер пуст, и ErrStopped после Stop.
func (r *Ring) ReadWait(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.n == 0 && !r.closed && !r.stopped && !r.wake {
		r.cond.Wait()
	}
	if r.wake {
		r.wake = false
		return 0, ErrWake
	}
	if r.stopped {
		return 0, ErrStopped
	}
	if r.n == 0 {
		return 0, io.EOF
	}
	k := min(len(p), r.n, len(r.buf)-r.r)
	copy(p, r.buf[r.r:r.r+k])
	r.r = (r.r + k) % len(r.buf)
	r.n -= k
	r.hist = min(r.hist+k, len(r.buf)-r.n)
	r.cond.Broadcast()
	return k, nil
}

// Rewind возвращает чтение назад на k байт (но не дальше сохранившейся истории) и сообщает, на сколько вернул.
func (r *Ring) Rewind(k int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	k = min(k, r.hist)
	if k <= 0 {
		return 0
	}
	r.r = (r.r - k + len(r.buf)) % len(r.buf)
	r.n += k
	r.hist -= k
	r.cond.Broadcast()
	return k
}

// WaitLevel ждёт, пока в буфере будет не меньше want байт или писатель закончит. false — буфер брошен.
func (r *Ring) WaitLevel(want int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.n < want && !r.closed && !r.stopped {
		r.cond.Wait()
	}
	return !r.stopped
}

func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// CloseWrite сообщает, что данных больше не будет.
func (r *Ring) CloseWrite() {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Stop бросает буфер: все ожидающие просыпаются с ошибкой.
func (r *Ring) Stop() {
	r.mu.Lock()
	r.stopped = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Wake будит одно ожидающее чтение (оно вернёт ErrWake), не теряя данных.
func (r *Ring) Wake() {
	r.mu.Lock()
	r.wake = true
	r.cond.Broadcast()
	r.mu.Unlock()
}
