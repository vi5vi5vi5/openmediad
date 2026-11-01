package spotifysoloist

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/spectrum"
	"openmediad/internal/libs/pcmstream"
)

// Протокол сокета шима (подробно — в шапке shim/libpulse_shim.c): привет «OMDP», версия, каналы, частота; затем кадры
// «тип, длина, данные». Звук всегда S16LE.
const (
	frData   = 0x01 // звук
	frFlush  = 0x02 // выбросить всё, что пришло раньше
	frCork   = 0x03 // 1 байт: 1 — пауза, 0 — продолжение
	frVolume = 0x04 // 2 байта: усиление в тысячных (для сведения)

	maxFrame     = 1 << 20
	helloVersion = 1

	// Сколько звука держим между сокетом и движком. Чем меньше, тем быстрее пауза и перемотка, чем больше, тем устойчивее
	// к рывкам: шим буферизует ещё 0,5 с, а движок и aplay — свои 0,2–0,5 с.
	maxBuffered = 300 * time.Millisecond
	history     = 800 * time.Millisecond // для возврата непрозвучавшего звука при паузе
)

// readHello читает привет шима и возвращает формат потока.
func readHello(r io.Reader) (audio.Format, error) {
	var h [10]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return audio.Format{}, fmt.Errorf("привет шима: %w", err)
	}
	if string(h[:4]) != "OMDP" || h[4] != helloVersion {
		return audio.Format{}, fmt.Errorf("привет шима: не тот протокол (%q, версия %d)", h[:4], h[4])
	}
	f := audio.Format{Channels: int(h[5]), Rate: int(binary.LittleEndian.Uint32(h[6:]))}
	if f.Channels < 1 || f.Channels > 2 || f.Rate < 8000 || f.Rate > 192000 {
		return audio.Format{}, fmt.Errorf("привет шима: формат %d Гц, %d кан. не поддержан", f.Rate, f.Channels)
	}
	return f, nil
}

// readFrame читает один кадр; buf используется повторно, если его хватает (кадр действителен до следующего вызова).
func readFrame(r io.Reader, buf *[]byte) (typ byte, payload []byte, err error) {
	var h [5]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(h[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("кадр шима %d байт — слишком большой", n)
	}
	if cap(*buf) < int(n) {
		*buf = make([]byte, n)
	}
	p := (*buf)[:n]
	if _, err = io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

// pcmServer принимает звук от шима: на каждое соединение (поток pa_stream Soloist) заводится поток движка класса music.
type pcmServer struct {
	path    string
	eng     *engine.Engine
	logf    func(string, ...any)
	latency time.Duration // задержка от шима до динамика: вычитается из позиции по часам
	onEvent func()        // состояние изменилось (играет / пауза): для арбитража и статуса

	mu       sync.Mutex
	ln       net.Listener
	sessions map[*session]struct{}
	seq      int
	closing  bool
	wg       sync.WaitGroup

	connects, flushes uint64
	bytesIn           uint64
	lastVolume        int // тысячные, -1 — не сообщали
}

func newPCMServer(path string, eng *engine.Engine, logf func(string, ...any)) *pcmServer {
	return &pcmServer{path: path, eng: eng, logf: logf, sessions: map[*session]struct{}{}, lastVolume: -1}
}

// Start открывает сокет и принимает соединения шима.
func (s *pcmServer) Start() error {
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		return err
	}
	os.Remove(s.path) // остаток прошлого запуска
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("сокет звука %s: %w", s.path, err)
	}
	os.Chmod(s.path, 0o600) // только root: чужой процесс не должен подсовывать звук
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.accept(ln)
	return nil
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == os.PathSeparator {
			return p[:i]
		}
	}
	return "."
}

func (s *pcmServer) accept(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

// Stop закрывает сокет и все потоки; возвращается, когда вывод остановлен.
func (s *pcmServer) Stop() {
	s.mu.Lock()
	s.closing = true
	ln := s.ln
	var list []*session
	for ss := range s.sessions {
		list = append(list, ss)
	}
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, ss := range list {
		ss.conn.Close()
		ss.finish("остановка", true)
	}
	s.wg.Wait()
	os.Remove(s.path)
}

// Playing — звучит ли сейчас хоть один поток (не на паузе и поток движка играет).
func (s *pcmServer) Playing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ss := range s.sessions {
		if ss.isPlaying() {
			return true
		}
	}
	return false
}

// pcmStatus — снимок для статуса.
type pcmStatus struct {
	Streams   int    `json:"streams"`
	Playing   bool   `json:"playing"`
	Connects  uint64 `json:"connects"`
	Flushes   uint64 `json:"flushes"`
	BytesIn   uint64 `json:"bytes_in"`
	BufferedB int    `json:"buffered_bytes"`
	GainMille int    `json:"gain_permille"`
	Format    string `json:"format,omitempty"`
}

func (s *pcmServer) status() pcmStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := pcmStatus{Streams: len(s.sessions), Connects: s.connects, Flushes: s.flushes, BytesIn: s.bytesIn, GainMille: s.lastVolume}
	for ss := range s.sessions {
		st.Playing = st.Playing || ss.isPlaying()
		st.BufferedB += ss.src.Buffered()
		st.Format = fmt.Sprintf("%d Гц, %d кан.", ss.f.Rate, ss.f.Channels)
	}
	return st
}

func (s *pcmServer) changed() {
	if s.onEvent != nil {
		s.onEvent()
	}
}

// handle ведёт одно соединение шима до его закрытия.
func (s *pcmServer) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 64<<10)
	f, err := readHello(r)
	if err != nil {
		s.logf("%v", err)
		return
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.seq++
	id := fmt.Sprintf("spotify/%d", s.seq)
	s.connects++
	s.mu.Unlock()

	ss := &session{srv: s, id: id, conn: conn, f: f, src: pcmstream.New(f, maxBuffered, history), corked: true}
	ss.st = s.eng.NewStream(id, engine.Music, ss.src,
		engine.Options{Spectrum: spectrum.SourceMusic, RewindOnPause: true, Endless: true, PosLatency: s.latency, FadeIn: 25 * time.Millisecond},
		engine.Events{
			Ended: func() { go ss.finish("поток доигран", false) },
			Failed: func(err error) {
				s.logf("поток %s: %v", id, err)
				go ss.finish("ошибка вывода", false)
			},
		})
	s.mu.Lock()
	s.sessions[ss] = struct{}{}
	s.mu.Unlock()
	s.logf("поток Spotify %s открыт: %d Гц, %d кан.", id, f.Rate, f.Channels)

	var buf []byte
	for {
		typ, p, err := readFrame(r, &buf)
		if err != nil {
			break
		}
		ss.frame(typ, p)
	}
	// шим закрыл сокет (трек кончился или Soloist остановился): доиграть остаток, если звук шёл, иначе закрыть сразу
	ss.src.CloseWrite()
	if !ss.isPlaying() {
		ss.finish("шим отключился", false)
		return
	}
	ss.waitDone()
}

// session — одно соединение шима и поток движка к нему.
type session struct {
	srv  *pcmServer
	id   string
	conn net.Conn
	f    audio.Format
	src  *pcmstream.Source
	st   *engine.Stream

	mu      sync.Mutex
	corked  bool // шим сообщил о паузе (пока не сообщил иначе — на паузе)
	playing bool // поток движка запущен (Play без Pause)
	done    chan struct{}
	closed  bool
}

func (ss *session) isPlaying() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.playing && !ss.corked && !ss.closed
}

func (ss *session) frame(typ byte, p []byte) {
	s := ss.srv
	switch typ {
	case frData:
		if fb := ss.f.FrameBytes(); len(p)%fb != 0 {
			p = p[:len(p)-len(p)%fb]
		}
		s.mu.Lock()
		s.bytesIn += uint64(len(p))
		s.mu.Unlock()
		ss.start() // звук пошёл без явного снятия паузы: считаем, что пауза снята
		ss.src.Write(p)
	case frFlush:
		s.mu.Lock()
		s.flushes++
		s.mu.Unlock()
		ss.src.Flush()
	case frCork:
		if len(p) < 1 {
			return
		}
		if p[0] == 0 {
			ss.start()
		} else {
			ss.pause()
		}
	case frVolume:
		if len(p) >= 2 {
			s.mu.Lock()
			s.lastVolume = int(binary.LittleEndian.Uint16(p))
			s.mu.Unlock()
		}
	}
}

func (ss *session) start() {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return
	}
	was := ss.playing && !ss.corked
	ss.corked, ss.playing = false, true
	ss.mu.Unlock()
	ss.st.Play()
	if !was {
		ss.srv.changed()
	}
}

func (ss *session) pause() {
	ss.mu.Lock()
	was := ss.playing && !ss.corked
	ss.corked, ss.playing = true, false
	ss.mu.Unlock()
	ss.st.Pause()
	if was {
		ss.srv.changed()
	}
}

// waitDone ждёт конца потока (Ended от движка или остановка).
func (ss *session) waitDone() {
	ss.mu.Lock()
	if ss.done == nil {
		ss.done = make(chan struct{})
	}
	d, closed := ss.done, ss.closed
	ss.mu.Unlock()
	if !closed {
		<-d
	}
}

// finish закрывает поток движка и убирает соединение; wait — дождаться остановки вывода.
func (ss *session) finish(why string, wait bool) {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return
	}
	ss.closed, ss.playing = true, false
	if ss.done == nil {
		ss.done = make(chan struct{})
	}
	done := ss.done
	ss.mu.Unlock()
	ss.conn.Close()
	ss.src.CloseWrite()
	if wait {
		ss.st.CloseWait()
	} else {
		ss.st.Close()
	}
	ss.srv.mu.Lock()
	delete(ss.srv.sessions, ss)
	ss.srv.mu.Unlock()
	ss.srv.logf("поток Spotify %s закрыт (%s)", ss.id, why)
	close(done)
	ss.srv.changed()
}

var errClosed = errors.New("закрыто")
