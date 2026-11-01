package pcmstream

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
)

var f48 = audio.Format{Rate: 48000, Channels: 2} // кадр 4 байта, 192 КБ/с

func frames(n int, v byte) []byte { return bytes.Repeat([]byte{v, v, v, v}, n) }

// Источник удовлетворяет интерфейсам движка.
var (
	_ engine.Source   = (*Source)(nil)
	_ engine.Waker    = (*Source)(nil)
	_ engine.Rewinder = (*Source)(nil)
)

func readAll(t *testing.T, s *Source, n int) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 400)
	for len(out) < n {
		want := n - len(out)
		if want > len(buf) {
			want = len(buf)
		}
		k, err := s.Read(buf[:want])
		if err != nil {
			t.Fatalf("чтение после %d байт: %v", len(out), err)
		}
		out = append(out, buf[:k]...)
	}
	return out
}

func TestWriteReadWholeFrames(t *testing.T) {
	s := New(f48, time.Second, 0)
	if _, err := s.Write(append(frames(5, 1), 9, 9)); err != nil { // хвост в полкадра
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	n, err := s.Read(buf)
	if err != nil || n != 20 {
		t.Fatalf("прочитано %d, %v; ждали 20 (5 кадров)", n, err)
	}
}

func TestBackPressure(t *testing.T) {
	s := New(f48, 10*time.Millisecond, 0) // потолок 1920 байт
	done := make(chan struct{})
	go func() {
		s.Write(frames(2000, 7)) // 8000 байт
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Write не заблокировался на полном буфере")
	case <-time.After(50 * time.Millisecond):
	}
	if b := s.Buffered(); b > 1920 {
		t.Fatalf("накоплено %d байт при потолке 1920", b)
	}
	got := readAll(t, s, 8000)
	if len(got) != 8000 {
		t.Fatalf("прочитано %d", len(got))
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write не доделался после чтения")
	}
}

func TestFlushDropsPending(t *testing.T) {
	s := New(f48, time.Second, time.Second)
	s.Write(frames(100, 1))
	s.Flush()
	if s.Buffered() != 0 {
		t.Fatal("после Flush остался звук")
	}
	if s.Rewind(100) != 0 {
		t.Fatal("после Flush осталась история")
	}
	// Flush будит писателя, который ждёт места, и не пускает в буфер устаревший остаток
	s2 := New(f48, 10*time.Millisecond, 0)
	s2.Write(frames(480, 1))
	done := make(chan int)
	go func() { n, _ := s2.Write(frames(2000, 2)); done <- n }()
	time.Sleep(30 * time.Millisecond)
	s2.Flush()
	select {
	case n := <-done:
		if n != 8000 {
			t.Fatalf("Write вернул %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("Flush не разбудил писателя")
	}
	if s2.Buffered() != 0 {
		t.Fatalf("устаревший остаток попал в буфер: %d", s2.Buffered())
	}
}

func TestEOFAfterDrain(t *testing.T) {
	s := New(f48, time.Second, 0)
	s.Write(frames(10, 3))
	s.CloseWrite()
	if got := readAll(t, s, 40); len(got) != 40 {
		t.Fatal("остаток не отдан")
	}
	if _, err := s.Read(make([]byte, 40)); err != io.EOF {
		t.Fatalf("после остатка: %v, ждали io.EOF", err)
	}
	if _, err := s.Write(frames(1, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("запись после CloseWrite: %v", err)
	}
}

func TestWakeAndClose(t *testing.T) {
	s := New(f48, time.Second, 0)
	res := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 40)); res <- err }()
	time.Sleep(20 * time.Millisecond)
	s.Wake()
	select {
	case err := <-res:
		if !errors.Is(err, audio.ErrWake) {
			t.Fatalf("Wake: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wake не разбудил чтение")
	}
	go func() { _, err := s.Read(make([]byte, 40)); res <- err }()
	time.Sleep(20 * time.Millisecond)
	s.Close()
	select {
	case err := <-res:
		if err != io.EOF {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close не разбудил чтение")
	}
}

func TestRewindReturnsPlayedTail(t *testing.T) {
	s := New(f48, time.Second, 100*time.Millisecond) // история 19200 байт
	s.Write(frames(10, 1))
	s.Write(frames(10, 2))
	got := readAll(t, s, 40) // 10 кадров «1» + 10 «2» = 80 байт; читаем первые 40
	if !bytes.Equal(got, frames(10, 1)) {
		t.Fatal("не те первые данные")
	}
	if k := s.Rewind(20); k != 20 {
		t.Fatalf("Rewind вернул %d, ждали 20", k)
	}
	all := readAll(t, s, 60)
	want := append(frames(5, 1), frames(10, 2)...)
	if !bytes.Equal(all, want) {
		t.Fatalf("после Rewind порядок нарушен")
	}
	if k := s.Rewind(6); k != 4 { // 6 байт = 1 кадр (4) и хвост; кратно кадру
		t.Fatalf("Rewind(6) вернул %d, ждали 4", k)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := New(f48, time.Second, 10*time.Millisecond) // история 1920 байт
	s.Write(frames(2400, 5))
	readAll(t, s, 9600)
	if k := s.Rewind(1 << 20); k != 1920 {
		t.Fatalf("история %d байт, ждали 1920", k)
	}
}
