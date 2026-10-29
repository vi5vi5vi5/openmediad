package yandexmusic

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/components/yandex-music/aac"
	"openmediad/internal/components/yandex-music/mp4/mp4test"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/spectrum"
)

const fakeFrameBytes = 4096 // PCM на один кадр AAC у поддельного декодера

// fakeDec «декодирует»: на каждый кадр ADTS отдаёт fakeFrameBytes PCM, в первых отсчётах — номер кадра (первый байт полезной нагрузки).
type fakeDec struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	closed atomic.Bool
}

func newFakeDec() *fakeDec {
	pr, pw := io.Pipe()
	return &fakeDec{pr: pr, pw: pw}
}

func (d *fakeDec) Write(p []byte) (int, error) {
	pcm := make([]byte, fakeFrameBytes)
	binary.LittleEndian.PutUint16(pcm, uint16(p[7])) // payload[0] — номер кадра (так собран synthMP4)
	if _, err := d.pw.Write(pcm); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (d *fakeDec) Read(p []byte) (int, error) { return d.pr.Read(p) }
func (d *fakeDec) CloseWrite() error          { return d.pw.Close() }
func (d *fakeDec) Close() error {
	d.closed.Store(true)
	d.pw.CloseWithError(io.ErrClosedPipe)
	d.pr.CloseWithError(io.ErrClosedPipe)
	return nil
}

// memSink собирает PCM, который «играл» один aplay (выход движка).
type memSink struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	aborted  atomic.Bool
	finished atomic.Bool
	delay    time.Duration
}

func (m *memSink) Write(p []byte) (int, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}
func (m *memSink) Abort() { m.aborted.Store(true) }
func (m *memSink) Drain() { m.finished.Store(true) }
func (m *memSink) bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.buf.Bytes()...)
}

// frames — номера кадров в PCM (по первому отсчёту каждого блока fakeFrameBytes).
func frames(pcm []byte) (out []int) {
	for i := 0; i+fakeFrameBytes <= len(pcm); i += fakeFrameBytes {
		out = append(out, int(binary.LittleEndian.Uint16(pcm[i:])))
	}
	return
}

type harness struct {
	b      *Backend
	eng    *engine.Engine
	srv    *httptest.Server
	mu     sync.Mutex
	sinks  []*memSink
	decs   []*fakeDec
	opened chan uint64
	failed chan error
	done   chan string
	key    []byte
	n      int
	paced  time.Duration // задержка каждой записи в приёмник: имитирует вывод в реальном времени
}

func newHarness(t *testing.T, n int, status int) *harness {
	h := &harness{opened: make(chan uint64, 4), failed: make(chan error, 4), done: make(chan string, 4), key: []byte("0123456789abcdef"), n: n}
	file, _ := mp4test.Synth(n, func(i int) uint32 { return 120 })
	// payload[0] каждого кадра = i (synthMP4 заполняет кадр байтом i); для i ≥ 256 номер усечётся — тесты используют n < 256
	enc := encrypt(h.key, file)
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			http.Error(w, "нет", status)
			return
		}
		http.ServeContent(w, r, "t.mp4", time.Time{}, bytes.NewReader(enc))
	}))
	t.Cleanup(h.srv.Close)
	h.eng = &engine.Engine{
		Router: engine.RouterFunc(func(engine.Class, audio.Format) (audio.Output, error) {
			s := &memSink{delay: h.paced}
			h.mu.Lock()
			h.sinks = append(h.sinks, s)
			h.mu.Unlock()
			return s, nil
		}),
		Logf: func(f string, a ...any) { t.Logf(f, a...) },
	}
	h.b = &Backend{
		Engine: h.eng,
		Events: h,
		Client: h.srv.Client(),
		NewDecoder: func() (aac.Decoder, error) {
			d := newFakeDec()
			h.mu.Lock()
			h.decs = append(h.decs, d)
			h.mu.Unlock()
			return d, nil
		},
		NoFadeIn: true,
		NoRewind: true, // мгновенный приёмник ничего не «теряет» в буферах: оценка потерь по часам здесь не применима
		Logf:     func(f string, a ...any) { t.Logf(f, a...) },
	}
	t.Cleanup(h.b.Shutdown)
	return h
}

// события Backend для автомата mediad
func (h *harness) Opened(id string, d uint64)  { h.opened <- d }
func (h *harness) Failed(id string, err error) { h.failed <- err }
func (h *harness) Finished(id string)          { h.done <- id }

func (h *harness) track(id string) mediad.Track {
	return mediad.Track{ID: "t", URL: h.srv.URL, Key: h.key, Title: "тест"}
}

func (h *harness) allPCM() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []byte
	for _, s := range h.sinks {
		out = append(out, s.bytes()...)
	}
	return out
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("не дождались: %s", what)
		panic("unreachable")
	}
}

func TestPlayWholeTrackInOrder(t *testing.T) {
	h := newHarness(t, 40, 0)
	h.b.Open("p1", h.track("p1"), 0)
	dur := waitFor(t, h.opened, "Opened")
	if want := uint64(40 * 1024 * 1000 / 48000); dur != want {
		t.Fatalf("длительность %d мс, ждали %d", dur, want)
	}
	h.b.Start("p1")
	if id := waitFor(t, h.done, "Finished"); id != "p1" {
		t.Fatalf("Finished для %q", id)
	}
	got := frames(h.allPCM())
	if len(got) != 40 {
		t.Fatalf("кадров воспроизведено %d, ждали 40", len(got))
	}
	for i, f := range got {
		if f != i {
			t.Fatalf("кадр %d в позиции %d: порядок нарушен", f, i)
		}
	}
	if !h.sinks[0].finished.Load() || h.sinks[0].aborted.Load() {
		t.Fatal("в конце трека aplay должен доиграть (Finish), а не обрываться")
	}
}

func TestOpenedBeforeStartIsBackpressured(t *testing.T) {
	// Трек длиннее буфера: без Start вывод не идёт, а скачивание/декодер встают на заполненном буфере.
	h := newHarness(t, 200, 0)
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	time.Sleep(300 * time.Millisecond)
	if len(h.allPCM()) != 0 {
		t.Fatal("до Start звук выводиться не должен")
	}
	tr := h.b.get("p1")
	if got := tr.src.sess.ring.Len(); got < openLevel || got > ringBytes {
		t.Fatalf("в буфере %d байт, ждали от %d до %d", got, openLevel, ringBytes)
	}
}

func TestSeekStartsAtRightFrame(t *testing.T) {
	h := newHarness(t, 120, 0)
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Seek("p1", 1000) // 1000 мс → кадр 46 (кадр = 21,333 мс)
	time.Sleep(100 * time.Millisecond)
	h.b.Start("p1")
	waitFor(t, h.done, "Finished")
	got := frames(h.allPCM())
	if len(got) == 0 || got[0] != 46 {
		t.Fatalf("после перемотки на 1000 мс первым должен играть кадр 46, а играет %v", got[:min(3, len(got))])
	}
	if want := 120 - 46; len(got) != want {
		t.Fatalf("воспроизведено %d кадров, ждали %d", len(got), want)
	}
	if pos := h.b.Pos("p1"); pos < 2400 || pos > 2600 {
		t.Fatalf("после доигрывания позиция %d мс, ждали ≈2560 (длительность трека)", pos)
	}
}

func TestPauseResumeKeepsStreamContinuous(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	time.Sleep(50 * time.Millisecond)
	h.b.Pause("p1")
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	first := h.sinks[0]
	h.mu.Unlock()
	if !first.aborted.Load() {
		t.Fatal("пауза должна обрывать aplay сразу")
	}
	posAtPause := h.b.Pos("p1")
	time.Sleep(150 * time.Millisecond)
	if p := h.b.Pos("p1"); p != posAtPause {
		t.Fatalf("на паузе позиция %d сдвинулась до %d", posAtPause, p)
	}
	h.b.Start("p1")
	waitFor(t, h.done, "Finished")
	got := frames(h.allPCM())
	if len(got) < 100 {
		t.Fatalf("воспроизведено слишком мало кадров: %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("разрыв или повтор после паузы: кадр %d идёт за %d", got[i], got[i-1])
		}
	}
}

func TestAdoptKeepsBufferWhenOffsetMatches(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	time.Sleep(50 * time.Millisecond)
	h.b.Pause("p1")
	time.Sleep(50 * time.Millisecond)
	pos := h.b.Pos("p1")
	h.mu.Lock()
	ndec := len(h.decs)
	h.mu.Unlock()
	h.b.Adopt("p1", "p2", pos)
	if h.b.get("p1") != nil || h.b.get("p2") == nil {
		t.Fatal("источник должен перейти к новому id")
	}
	h.b.Start("p2")
	if id := waitFor(t, h.done, "Finished"); id != "p2" {
		t.Fatalf("Finished для %q, ждали p2", id)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decs) != ndec {
		t.Fatal("возобновление с тем же смещением не должно перезапускать декодер")
	}
}

func TestOpenFailureReported(t *testing.T) {
	h := newHarness(t, 10, 404)
	h.b.Open("p1", h.track("p1"), 0)
	err := waitFor(t, h.failed, "OnFailed")
	if err == nil {
		t.Fatal("ожидали ошибку открытия")
	}
	select {
	case <-h.opened:
		t.Fatal("Opened не должен приходить для неоткрывшегося потока")
	default:
	}
}

func TestCloseTearsEverythingDown(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	time.Sleep(50 * time.Millisecond)
	h.b.Close("p1")
	time.Sleep(150 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.decs[0].closed.Load() {
		t.Fatal("декодер должен быть закрыт")
	}
	if !h.sinks[0].aborted.Load() {
		t.Fatal("вывод должен быть оборван")
	}
	select {
	case id := <-h.done:
		t.Fatalf("после Close не должно быть Finished (%s)", id)
	default:
	}
}

// specLog собирает события спектра движка.
type specLog struct {
	mu sync.Mutex
	ev []spectrum.Event
}

func (s *specLog) add(e spectrum.Event) {
	e.DB = append([]float32(nil), e.DB...)
	s.mu.Lock()
	s.ev = append(s.ev, e)
	s.mu.Unlock()
}

func (s *specLog) all() []spectrum.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spectrum.Event(nil), s.ev...)
}

func TestSpectrumFollowsPlayback(t *testing.T) {
	h := newHarness(t, 60, 0)
	var sl specLog
	h.eng.OnSpectrum = sl.add
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	waitFor(t, h.done, "Finished")
	ev := sl.all()
	if len(ev) < 3 || ev[0].Kind != spectrum.KindStart {
		t.Fatalf("первым должно идти сообщение о старте потока, а пришло %+v", ev[:min(2, len(ev))])
	}
	if ev[len(ev)-1].Kind != spectrum.KindStop {
		t.Fatalf("последним — остановка потока, а пришло %d", ev[len(ev)-1].Kind)
	}
	data := 0
	for _, e := range ev {
		if e.Stream != ev[0].Stream {
			t.Fatal("у одного трека один идентификатор потока")
		}
		if e.Kind == spectrum.KindData {
			data++
			if len(e.DB) != spectrum.Bands {
				t.Fatalf("кадр из %d значений вместо %d", len(e.DB), spectrum.Bands)
			}
		}
	}
	// 60 кадров AAC по 4096 Б PCM = 245760 Б = 61440 отсчётов = 32 интервала по 1920 отсчётов
	if want := 60 * fakeFrameBytes / 4 / spectrum.FrameLen; data != want {
		t.Fatalf("кадров спектра %d, ждали %d", data, want)
	}
}

func TestSpectrumPauseStopsAndResumeContinuesStream(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond
	var sl specLog
	h.eng.OnSpectrum = sl.add
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	time.Sleep(80 * time.Millisecond)
	h.b.Pause("p1")
	time.Sleep(100 * time.Millisecond)
	h.b.Start("p1")
	time.Sleep(80 * time.Millisecond)
	h.b.Close("p1")
	time.Sleep(100 * time.Millisecond)
	var kinds []int
	var stream uint64
	for _, e := range sl.all() {
		if e.Kind == spectrum.KindData {
			continue
		}
		kinds = append(kinds, e.Kind)
		if stream == 0 {
			stream = e.Stream
		}
		if e.Stream != stream {
			t.Fatal("возобновление должно продолжать тот же поток")
		}
	}
	want := []int{spectrum.KindStart, spectrum.KindStop, spectrum.KindResume, spectrum.KindStop}
	if len(kinds) != len(want) {
		t.Fatalf("служебные сообщения %v, ждали %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("служебные сообщения %v, ждали %v", kinds, want)
		}
	}
}

func TestDecoderStartFailureReported(t *testing.T) {
	h := newHarness(t, 20, 0)
	h.b.NewDecoder = func() (aac.Decoder, error) { return nil, io.ErrUnexpectedEOF }
	h.b.Open("p1", h.track("p1"), 0)
	if err := waitFor(t, h.failed, "OnFailed"); err == nil {
		t.Fatal("ошибка запуска декодера должна сообщаться (откат на mediad)")
	}
}

func TestReopenOfOpenedTrackRenotifies(t *testing.T) {
	h := newHarness(t, 30, 0)
	h.b.Open("p1", h.track("p1"), 0)
	d1 := waitFor(t, h.opened, "первое Opened")
	h.b.Open("p1", h.track("p1"), 0) // повторный prefetch
	d2 := waitFor(t, h.opened, "повторное Opened")
	if d1 != d2 {
		t.Fatalf("длительность в повторном уведомлении %d, в первом %d", d2, d1)
	}
	h.mu.Lock()
	n := len(h.decs)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("повторное открытие не должно запускать второй декодер, запущено %d", n)
	}
}

// Пауза выбрасывает то, что лежало в трубе и буферах aplay; шим возвращает это в буфер, и после «продолжи» звук идёт с места,
// где реально замолчал динамик. Часы подменены: «прошло» 400 мс при «записано» около 240 КБ (1,2 с звука).
func TestPauseRewindsAudioThatNeverReachedSpeaker(t *testing.T) {
	h := newHarness(t, 150, 0)
	h.paced = 8 * time.Millisecond // вывод в 6 раз быстрее реального времени: записано больше, чем прозвучало бы
	h.b.NoRewind = false
	var clk atomic.Int64 // прошедшее время цикла вывода, нс
	base := time.Now()
	h.eng.Now = func() time.Time { return base.Add(time.Duration(clk.Load())) }
	h.b.Open("p1", h.track("p1"), 0)
	waitFor(t, h.opened, "Opened")
	h.b.Start("p1")
	const audible = 360 * bytesPerMs
	// часы стоят, пока вывод пишет: ждём, пока записано заметно больше, чем «прозвучит» (сон здесь зависел бы от нагрузки)
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.sinks)
		h.mu.Unlock()
		if n > 0 && len(h.sinks[0].bytes()) >= audible+19200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("вывод не начался")
		}
		time.Sleep(5 * time.Millisecond)
	}
	clk.Store(int64(400 * time.Millisecond)) // прозвучало 400 − 40 (задержка) = 360 мс = 69120 байт
	h.b.Pause("p1")
	time.Sleep(150 * time.Millisecond)
	written := len(h.sinks[0].bytes())
	if written < audible+9600 {
		t.Fatalf("приёмник принял лишь %d байт: для проверки нужно заметно больше %d", written, audible)
	}
	if p := h.b.Pos("p1"); p < 355 || p > 365 {
		t.Fatalf("позиция после паузы %d мс, ждали ≈360 (то, что прозвучало), а не %d (то, что записано)", p, written/bytesPerMs)
	}
	h.b.Start("p1")
	waitFor(t, h.done, "Finished")
	second := h.sinks[1].bytes()
	if len(second) < 4096 {
		t.Fatalf("после «продолжи» второй приёмник получил лишь %d байт", len(second))
	}
	if !bytes.Equal(second[:4096], expectedStream(150)[audible:audible+4096]) {
		t.Fatalf("звук после паузы должен продолжаться с байта %d (то, что прозвучало), а продолжился с другого места", audible)
	}
}

// expectedStream — весь PCM трека у поддельного декодера: кадр i даёт fakeFrameBytes байт, первые два — номер кадра.
func expectedStream(frames int) []byte {
	out := make([]byte, frames*fakeFrameBytes)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(out[i*fakeFrameBytes:], uint16(i))
	}
	return out
}
