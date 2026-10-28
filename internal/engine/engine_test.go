package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/spectrum"
)

// memSource отдаёт data (по кругу, если loop), может ждать ворот перед первым чтением и ломаться после failAfter байт.
type memSource struct {
	f         audio.Format
	data      []byte
	loop      bool
	openErr   error
	openGate  chan struct{}
	failAfter int
	off       int
	read      int
	closed    atomic.Bool
}

func (m *memSource) Format() audio.Format { return m.f }
func (m *memSource) Open(ctx context.Context) (audio.Info, error) {
	if m.openGate != nil {
		select {
		case <-m.openGate:
		case <-ctx.Done():
			return audio.Info{}, ctx.Err()
		}
	}
	if m.openErr != nil {
		return audio.Info{}, m.openErr
	}
	return audio.Info{Duration: m.f.Duration(uint64(len(m.data)))}, nil
}
func (m *memSource) Read(p []byte) (int, error) {
	if m.failAfter > 0 && m.read >= m.failAfter {
		return 0, errors.New("источник сломался")
	}
	if m.off == len(m.data) {
		if !m.loop {
			return 0, io.EOF
		}
		m.off = 0
	}
	n := copy(p, m.data[m.off:])
	m.off += n
	m.read += n
	return n, nil
}
func (m *memSource) Close() error { m.closed.Store(true); return nil }

// memOut — вывод в память; delay имитирует темп воспроизведения, gate держит запись до открытия или обрыва.
type memOut struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	delay   time.Duration
	aborted atomic.Bool
	drained atomic.Bool
	failAt  int
}

func (o *memOut) Write(p []byte) (int, error) {
	if o.aborted.Load() {
		return 0, errors.New("вывод оборван")
	}
	if o.delay > 0 {
		time.Sleep(o.delay)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failAt > 0 && o.buf.Len() >= o.failAt {
		return 0, errors.New("aplay умер")
	}
	return o.buf.Write(p)
}
func (o *memOut) Drain() { o.drained.Store(true) }
func (o *memOut) Abort() { o.aborted.Store(true) }
func (o *memOut) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buf.Bytes()...)
}

type rig struct {
	e      *Engine
	mu     sync.Mutex
	outs   []*memOut
	mkOut  func() (*memOut, error)
	events chan string
}

func newRig(t *testing.T) *rig {
	r := &rig{events: make(chan string, 64)}
	r.e = &Engine{
		Router: RouterFunc(func(c Class, f audio.Format) (audio.Output, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			mk := r.mkOut
			if mk == nil {
				mk = func() (*memOut, error) { return &memOut{}, nil }
			}
			o, err := mk()
			if err != nil {
				return nil, err
			}
			r.outs = append(r.outs, o)
			return o, nil
		}),
		Logf: func(f string, a ...any) { t.Logf(f, a...) },
	}
	return r
}

func (r *rig) events4(id string) Events {
	return Events{
		Ready:   func(audio.Info) { r.events <- "ready " + id },
		Playing: func() { r.events <- "playing " + id },
		Ended:   func() { r.events <- "ended " + id },
		Failed:  func(err error) { r.events <- "failed " + id + ": " + err.Error() },
	}
}

func (r *rig) next(t *testing.T) string {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("не дождались события")
		return ""
	}
}

func (r *rig) out(i int) *memOut {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outs[i]
}

func pcm(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func TestPlaysToEndInOrderWithEventsOnce(t *testing.T) {
	r := newRig(t)
	src := &memSource{f: audio.Stereo48, data: pcm(50000)}
	s := r.e.NewStream("a", Music, src, Options{}, r.events4("a"))
	s.Play() // Play без Open открывает источник сам
	for _, want := range []string{"ready a", "playing a", "ended a"} {
		if ev := r.next(t); ev != want {
			t.Fatalf("ждали %q, пришло %q", want, ev)
		}
	}
	if !bytes.Equal(r.out(0).bytes(), src.data) || !r.out(0).drained.Load() || r.out(0).aborted.Load() {
		t.Fatal("весь звук по порядку, в конце вывод доигрывается, а не обрывается")
	}
	select {
	case ev := <-r.events:
		t.Fatalf("лишнее событие %q", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if s.Playing() {
		t.Fatal("после конца звука поток не играет")
	}
}

func TestOpenFailureIsFailedAndOutputClosed(t *testing.T) {
	r := newRig(t)
	s := r.e.NewStream("a", Voice, &memSource{f: audio.Mono48, openErr: errors.New("нет сети")}, Options{}, r.events4("a"))
	s.Play()
	if ev := r.next(t); ev != "failed a: нет сети" {
		t.Fatalf("пришло %q", ev)
	}
	time.Sleep(30 * time.Millisecond)
	if !r.out(0).aborted.Load() {
		t.Fatal("вывод, поднятый заранее, должен быть закрыт")
	}
}

func TestOutputStartFailureIsFailed(t *testing.T) {
	r := newRig(t)
	r.mkOut = func() (*memOut, error) { return nil, errors.New("нет aplay") }
	r.e.NewStream("a", Alert, &memSource{f: audio.Stereo48, data: pcm(1000)}, Options{}, r.events4("a")).Play()
	for {
		ev := r.next(t)
		if strings.HasPrefix(ev, "ready") {
			continue
		}
		if !strings.Contains(ev, "failed a: запуск вывода alert: нет aplay") {
			t.Fatalf("пришло %q", ev)
		}
		return
	}
}

// Ошибка после того, как звук уже шёл, — это конец потока, а не повод для отката: у mediad трек тоже «закончился бы».
func TestBreakAfterSoundIsEnded(t *testing.T) {
	r := newRig(t)
	r.mkOut = func() (*memOut, error) { return &memOut{failAt: 20000}, nil }
	r.e.NewStream("a", Alert, &memSource{f: audio.Stereo48, data: pcm(10000), loop: true}, Options{Endless: true}, r.events4("a")).Play()
	for _, want := range []string{"ready a", "playing a", "ended a"} {
		if ev := r.next(t); ev != want {
			t.Fatalf("ждали %q, пришло %q", want, ev)
		}
	}
}

// Вывод отказал посреди звука (отключились наушники): поток переезжает на следующий выход маршрута и звучит дальше —
// без ENDED, а порция, которую не принял старый вывод, уходит в новый (со стороны движка звук непрерывен).
func TestOutputFailureMovesStreamToNextOutput(t *testing.T) {
	r := newRig(t)
	n := 0
	r.mkOut = func() (*memOut, error) {
		n++
		if n == 1 {
			return &memOut{failAt: 20000}, nil
		}
		return &memOut{}, nil
	}
	data := pcm(60000)
	r.e.NewStream("a", Music, &memSource{f: audio.Stereo48, data: data}, Options{}, r.events4("a")).Play()
	for _, want := range []string{"ready a", "playing a", "ended a"} {
		if ev := r.next(t); ev != want {
			t.Fatalf("ждали %q, пришло %q", want, ev)
		}
	}
	if len(r.outs) != 2 {
		t.Fatalf("выходов открыто %d, ждали 2", len(r.outs))
	}
	got := append(r.out(0).bytes(), r.out(1).bytes()...)
	if !bytes.Equal(got, data) {
		t.Fatalf("звук после переезда не непрерывен: %d байт из %d", len(got), len(data))
	}
	if !r.out(0).aborted.Load() || !r.out(1).drained.Load() {
		t.Fatal("отказавший вывод обрывается, новый доигрывается")
	}
}

// Появился выход лучше (подключились наушники): играющий поток нужного класса заново выбирает выход и доигрывает в нём —
// без новых событий; потоки других классов и закрытые не трогаются.
func TestRerouteMovesPlayingStream(t *testing.T) {
	r := newRig(t)
	r.mkOut = func() (*memOut, error) { return &memOut{delay: 2 * time.Millisecond}, nil }
	a := r.e.NewStream("a", Music, &memSource{f: audio.Stereo48, data: pcm(400000)}, Options{}, r.events4("a"))
	a.Play()
	for _, want := range []string{"ready a", "playing a"} {
		if ev := r.next(t); ev != want {
			t.Fatalf("ждали %q, пришло %q", want, ev)
		}
	}
	closed := r.e.NewStream("b", Music, &memSource{f: audio.Stereo48, data: pcm(1000)}, Options{}, Events{})
	closed.Close()
	if n := r.e.Reroute(func(c Class) bool { return c == Voice }); n != 0 {
		t.Fatalf("музыку по маршруту голоса не трогаем: %d", n)
	}
	if n := r.e.Reroute(func(c Class) bool { return c == Music }); n != 1 {
		t.Fatalf("переведено %d, ждали 1", n)
	}
	if ev := r.next(t); ev != "ended a" {
		t.Fatalf("ждали конец без лишних событий, пришло %q", ev)
	}
	r.mu.Lock()
	n := len(r.outs)
	r.mu.Unlock()
	if n != 2 || !r.out(0).aborted.Load() || len(r.out(1).bytes()) == 0 || !r.out(1).drained.Load() {
		t.Fatalf("выходов %d: старый оборван, новый доигран", n)
	}
	if r.e.Reroute(func(Class) bool { return true }) != 0 {
		t.Fatal("доигравший поток не переводится")
	}
}

// Выхода на замену нет — поток кончается, как раньше (звучал — ENDED).
func TestOutputFailureWithoutOtherOutputEnds(t *testing.T) {
	r := newRig(t)
	n := 0
	r.mkOut = func() (*memOut, error) {
		n++
		if n == 1 {
			return &memOut{failAt: 20000}, nil
		}
		return nil, errors.New("выходов нет")
	}
	r.e.NewStream("a", Music, &memSource{f: audio.Stereo48, data: pcm(60000)}, Options{}, r.events4("a")).Play()
	for _, want := range []string{"ready a", "playing a", "ended a"} {
		if ev := r.next(t); ev != want {
			t.Fatalf("ждали %q, пришло %q", want, ev)
		}
	}
}

func TestCloseIsSilentAndCloseWaitStopsOutput(t *testing.T) {
	r := newRig(t)
	r.mkOut = func() (*memOut, error) { return &memOut{delay: 5 * time.Millisecond}, nil }
	src := &memSource{f: audio.Stereo48, data: pcm(10000), loop: true}
	s := r.e.NewStream("a", Alert, src, Options{Endless: true}, r.events4("a"))
	s.Play()
	r.next(t) // ready
	r.next(t) // playing
	s.CloseWait()
	if !r.out(0).aborted.Load() || !src.closed.Load() {
		t.Fatal("после CloseWait вывод оборван, источник закрыт")
	}
	select {
	case ev := <-r.events:
		t.Fatalf("после Close событий не бывает, а пришло %q", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCloseWhileOpeningStaysSilent(t *testing.T) {
	r := newRig(t)
	gate := make(chan struct{})
	s := r.e.NewStream("a", Alert, &memSource{f: audio.Stereo48, data: pcm(1000), openGate: gate}, Options{}, r.events4("a"))
	s.Play()
	time.Sleep(20 * time.Millisecond)
	s.Close()
	close(gate)
	time.Sleep(50 * time.Millisecond)
	if len(r.events) != 0 || len(r.out(0).bytes()) != 0 {
		t.Fatal("закрытый до открытия поток не играет и не жалуется")
	}
}

// Позиция не опережает часы: записанное в вывод ещё не прозвучало. А бесконечный источник не упирается в длительность.
func TestPositionFollowsTheClock(t *testing.T) {
	r := newRig(t)
	var clk atomic.Int64
	base := time.Now()
	r.e.Now = func() time.Time { return base.Add(time.Duration(clk.Load())) }
	src := &memSource{f: audio.Stereo48, data: pcm(audio.Stereo48.Bytes(time.Second)), loop: true}
	s := r.e.NewStream("a", Alert, src, Options{Endless: true}, r.events4("a"))
	s.Play()
	r.next(t)
	r.next(t)
	for end := time.Now().Add(3 * time.Second); len(r.out(0).bytes()) < audio.Stereo48.Bytes(3*time.Second); { // вывод мгновенный
		if time.Now().After(end) {
			t.Fatal("вывод не набрал 3 с звука")
		}
		time.Sleep(5 * time.Millisecond)
	}
	clk.Store(int64(300 * time.Millisecond))
	if p := s.Pos(); p != 300*time.Millisecond {
		t.Fatalf("позиция %s, ждали 300ms (по часам, а не по записанному)", p)
	}
	clk.Store(int64(2500 * time.Millisecond))
	if p := s.Elapsed(); p != 2500*time.Millisecond {
		t.Fatalf("бесконечный источник: %s, ждали 2.5s (длительность прохода 1 с не ограничивает)", p)
	}
	s.CloseWait()
}

func TestClassGainDucksPlayingMusicSmoothly(t *testing.T) {
	r := newRig(t)
	one := make([]byte, 4)
	binary.LittleEndian.PutUint16(one, 10000)
	binary.LittleEndian.PutUint16(one[2:], 10000)
	data := bytes.Repeat(one, audio.Stereo48.Bytes(3*time.Second)/4)
	r.mkOut = func() (*memOut, error) { return &memOut{delay: time.Millisecond}, nil }
	s := r.e.NewStream("a", Music, &memSource{f: audio.Stereo48, data: data}, Options{}, r.events4("a"))
	s.Play()
	r.next(t)
	r.next(t)
	r.e.SetClassGain(Music, 0.25)
	if g := r.e.ClassGain(Voice); g != 1 {
		t.Fatalf("приглушение музыки не трогает голос: %v", g)
	}
	r.next(t) // ended
	got := r.out(0).bytes()
	last := int16(binary.LittleEndian.Uint16(got[len(got)-4:]))
	if last != 2500 {
		t.Fatalf("в конце громкость 0,25: отсчёт %d, ждали 2500", last)
	}
	// переход плавный: нет скачка больше шага на порцию
	prev := int16(10000)
	for i := 0; i+4 <= len(got); i += 4 {
		v := int16(binary.LittleEndian.Uint16(got[i:]))
		if d := int(prev) - int(v); d > 10 {
			t.Fatalf("скачок громкости на байте %d: %d → %d", i, prev, v)
		}
		prev = v
	}
}

func TestSpectrumStartStopAroundSound(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var kinds []int
	r.e.OnSpectrum = func(ev spectrum.Event) {
		mu.Lock()
		if ev.Kind != spectrum.KindData {
			kinds = append(kinds, ev.Kind)
		}
		mu.Unlock()
	}
	s := r.e.NewStream("a", Music, &memSource{f: audio.Stereo48, data: pcm(audio.Stereo48.Bytes(200 * time.Millisecond))},
		Options{Spectrum: spectrum.SourceMusic}, r.events4("a"))
	s.Play()
	for ev := r.next(t); ev != "ended a"; ev = r.next(t) {
	}
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 2 || kinds[0] != spectrum.KindStart || kinds[1] != spectrum.KindStop {
		t.Fatalf("служебные сообщения спектра %v, ждали [старт, остановка]", kinds)
	}
}

func TestParseClass(t *testing.T) {
	for _, c := range []Class{Music, Voice, Alert} {
		if got, ok := ParseClass(c.String()); !ok || got != c {
			t.Fatalf("%s не разбирается обратно", c)
		}
	}
	if _, ok := ParseClass("nope"); ok {
		t.Fatal("незнакомый класс")
	}
}

// SpectrumAlways: голос Алисы договорил или диалог кончился (музыка снова в полную силу) — поток спектра играющей музыки
// объявляется заново, чтобы платформа снова рисовала её анимацию. Без настройки — нет.
func TestSpectrumAlwaysReannouncesMusic(t *testing.T) {
	for _, always := range []bool{false, true} {
		r := newRig(t)
		r.mkOut = func() (*memOut, error) { return &memOut{delay: 2 * time.Millisecond}, nil }
		var mu sync.Mutex
		starts := map[uint64]int{}
		r.e.SpectrumAlways = always
		r.e.OnSpectrum = func(ev spectrum.Event) {
			if ev.Kind == spectrum.KindStart {
				mu.Lock()
				starts[ev.Stream]++
				mu.Unlock()
			}
		}
		music := r.e.NewStream("m", Music, &memSource{f: audio.Stereo48, data: pcm(400000)}, Options{Spectrum: spectrum.SourceMusic}, r.events4("m"))
		music.Play()
		for _, want := range []string{"ready m", "playing m"} {
			if ev := r.next(t); ev != want {
				t.Fatalf("ждали %q, пришло %q", want, ev)
			}
		}
		voice := r.e.NewStream("v", Voice, &memSource{f: audio.Mono48, data: pcm(9600)}, Options{Spectrum: spectrum.SourceVoice}, r.events4("v"))
		voice.Play()
		for ev := r.next(t); ev != "ended v"; ev = r.next(t) {
		}
		r.e.SetClassGain(Music, 0.3)
		r.e.SetClassGain(Music, 1)
		music.Close()
		mu.Lock()
		var mid uint64
		for id, n := range starts {
			if n >= 1 && id != 0 {
				music.mu.Lock()
				if id == music.specID {
					mid = id
				}
				music.mu.Unlock()
			}
		}
		n := starts[mid]
		mu.Unlock()
		want := 1
		if always {
			want = 3 // старт, после голоса, после конца приглушения
		}
		if mid == 0 || n != want {
			t.Fatalf("always=%v: стартов спектра музыки %d, ждали %d (%v)", always, n, want, starts)
		}
	}
}

// SpectrumAlways: пока музыка играет, её поток спектра объявляется заново раз в spectrumRefresh (платформа гасит анимацию
// через 15 с после «начат»), но не пока звучит голос и не во время приглушения.
func TestSpectrumAlwaysRefreshesPeriodically(t *testing.T) {
	old := spectrumRefresh
	spectrumRefresh = 40 * time.Millisecond
	defer func() { spectrumRefresh = old }()
	r := newRig(t)
	r.mkOut = func() (*memOut, error) { return &memOut{delay: 2 * time.Millisecond}, nil }
	var mu sync.Mutex
	starts := 0
	r.e.SpectrumAlways = true
	r.e.OnSpectrum = func(ev spectrum.Event) {
		if ev.Kind == spectrum.KindStart && ev.Source == spectrum.SourceMusic {
			mu.Lock()
			starts++
			mu.Unlock()
		}
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return starts }
	m := r.e.NewStream("m", Music, &memSource{f: audio.Stereo48, data: pcm(48000 * 4 * 10), loop: true}, Options{Spectrum: spectrum.SourceMusic, Endless: true}, Events{})
	m.Play()
	defer m.Close()
	deadline := time.Now().Add(3 * time.Second)
	for count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count() < 3 {
		t.Fatalf("объявлений спектра музыки %d, ждали не меньше 3", count())
	}
	r.e.SetClassGain(Music, 0.3) // диалог: не объявляем
	time.Sleep(30 * time.Millisecond)
	n := count()
	time.Sleep(150 * time.Millisecond)
	if count() != n {
		t.Fatalf("во время приглушения спектр музыки объявлялся: %d → %d", n, count())
	}
}
