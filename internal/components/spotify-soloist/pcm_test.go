package spotifysoloist

import (
	"net"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*pcmServer, *outs) {
	t.Helper()
	eng, o := newEngine()
	var logs []string
	s := newPCMServer("", eng, func(f string, a ...any) { _ = logs })
	return s, o
}

// connect открывает «соединение шима»: сервер разбирает его в своей горутине.
func connect(t *testing.T, s *pcmServer) net.Conn {
	t.Helper()
	cli, srv := net.Pipe()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handle(srv)
	}()
	t.Cleanup(func() { cli.Close() })
	return cli
}

func send(t *testing.T, c net.Conn, parts ...[]byte) {
	t.Helper()
	for _, p := range parts {
		c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionPlaysAndEnds(t *testing.T) {
	s, o := newTestServer(t)
	c := connect(t, s)
	send(t, c, hello(48000, 2), cork(false), frame(frData, pcm(480)), frame(frData, pcm(480)))
	want := 2 * 480 * 4
	waitFor(t, "звук дошёл до вывода", func() bool { return o.total() == want })
	if !s.Playing() {
		t.Fatal("поток играет, а Playing() = false")
	}
	st := s.status()
	if st.Streams != 1 || st.Connects != 1 || st.BytesIn != uint64(want) || st.Format != "48000 Гц, 2 кан." {
		t.Fatalf("статус %+v", st)
	}
	c.Close() // шим закрыл сокет: конец трека
	waitFor(t, "поток закрыт после конца", func() bool { return s.status().Streams == 0 })
	if s.Playing() {
		t.Fatal("после конца Playing() = true")
	}
	if outs := o.all(); len(outs) != 1 || !outs[0].drained.Load() {
		t.Fatalf("вывод должен быть доигран (Drain), а не оборван: %+v", outs)
	}
}

func TestSessionCorkPausesAndResumes(t *testing.T) {
	s, o := newTestServer(t)
	c := connect(t, s)
	send(t, c, hello(48000, 2), cork(false), frame(frData, pcm(240)))
	waitFor(t, "первый звук", func() bool { return o.total() == 240*4 })

	send(t, c, cork(true))
	waitFor(t, "вывод оборван паузой", func() bool { return o.all()[0].aborted.Load() })
	waitFor(t, "Playing() = false на паузе", func() bool { return !s.Playing() })

	// на паузе шим ничего не шлёт, но ждёт продолжения: звук, пришедший к снятию паузы, должен прозвучать в новом выводе
	send(t, c, cork(false), frame(frData, pcm(240)))
	waitFor(t, "после снятия паузы открыт новый вывод", func() bool { return len(o.all()) == 2 })
	waitFor(t, "звук после паузы дошёл", func() bool { return o.all()[1].bytes() >= 240*4 })
	if !s.Playing() {
		t.Fatal("после снятия паузы Playing() = false")
	}
}

func TestSessionStartsOnDataWithoutUncork(t *testing.T) {
	s, o := newTestServer(t)
	c := connect(t, s)
	// шим при подключении сообщает состояние паузы; если звук пошёл без снятия паузы, считаем, что пауза снята
	send(t, c, hello(44100, 2), frame(frData, pcm(100)))
	waitFor(t, "звук без явного снятия паузы", func() bool { return o.total() == 400 })
}

func TestFlushCounted(t *testing.T) {
	s, _ := newTestServer(t)
	c := connect(t, s)
	send(t, c, hello(48000, 2), cork(true), frame(frFlush, nil))
	waitFor(t, "flush учтён", func() bool { return s.status().Flushes == 1 })
}

func TestVolumeReported(t *testing.T) {
	s, _ := newTestServer(t)
	c := connect(t, s)
	send(t, c, hello(48000, 2), frame(frVolume, []byte{0xF4, 0x01})) // 500‰
	waitFor(t, "громкость принята", func() bool { return s.status().GainMille == 500 })
}

func TestBadHelloRejected(t *testing.T) {
	for name, h := range map[string][]byte{
		"чужой протокол":   []byte("GET / HTTP"),
		"3 канала":         hello(48000, 3),
		"частота 100":      hello(100, 2),
		"другая версия":    append([]byte("OMDP\x09\x02"), 0x80, 0xBB, 0, 0),
		"оборванный пакет": []byte("OMD"),
	} {
		t.Run(name, func(t *testing.T) {
			s, o := newTestServer(t)
			c := connect(t, s)
			send(t, c, h)
			c.Close()
			time.Sleep(30 * time.Millisecond)
			if s.status().Streams != 0 || len(o.all()) != 0 {
				t.Fatal("поток создан по плохому привету")
			}
		})
	}
}

func TestStopClosesSessions(t *testing.T) {
	s, o := newTestServer(t)
	c := connect(t, s)
	send(t, c, hello(48000, 2), cork(false), frame(frData, pcm(240)))
	waitFor(t, "звук пошёл", func() bool { return o.total() > 0 })
	s.mu.Lock()
	s.closing = false
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop завис")
	}
	if s.status().Streams != 0 {
		t.Fatal("после Stop остались потоки")
	}
}

func TestOversizedFrameDropsConnection(t *testing.T) {
	s, _ := newTestServer(t)
	c := connect(t, s)
	big := []byte{frData, 0xFF, 0xFF, 0xFF, 0x7F} // длина 2 ГБ
	send(t, c, hello(48000, 2), big)
	waitFor(t, "поток закрыт после битого кадра", func() bool { return s.status().Streams == 0 })
}
