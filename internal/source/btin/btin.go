// Package btin — источник «звук с телефона по Bluetooth» (опытный, BUS-PROTOCOL.md §2.7в). Штатно звук телефона играет
// bluealsa-aplay: PCM BlueALSA приёма A2DP → ALSA-устройство «bluetooth» → динамики, мимо openmediad. Пока компонент
// работает, bluealsa-aplay остановлен, а PCM телефона читает openmediad (системный arecord, как aplay на выводе) и играет
// его своим движком как музыку: с приглушением на время диалога по настройке focus.duck_gain, спектром для дисплея и
// маршрутом [route] music.
//
// Управление остаётся платформе: она по-прежнему видит звук телефона (Bluetooth-плеер, логотип), передаёт ему по AVRCP
// «пауза», «дальше», громкость, ведёт спаривание. Своё приглушение на время диалога она делает программной громкостью
// BlueALSA (SoftVolume = true, громкость транспорта 21) — тогда движок получил бы уже приглушённый звук и приглушил бы его
// ещё раз, поэтому источник сразу выключает программную громкость: приглушает движок. Саму громкость не трогает: её
// BlueALSA передаёт телефону по AVRCP (ползунок на телефоне), и пусть она остаётся такой, какой её ставит платформа.
package btin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/engine"
	"openmediad/internal/sink/alsa"
	"openmediad/internal/spectrum"
	"openmediad/internal/system/bluez"
	"openmediad/internal/system/dbus"
)

// Client — то, чем источник пользуется из system/bluez (подменяется в тестах).
type Client interface {
	State(ctx context.Context) (bluez.State, error)
	Watch(ctx context.Context) (<-chan struct{}, error)
	SetPCMSoftVolume(ctx context.Context, pcm dbus.ObjectPath, on bool) error
	Close()
}

// Source — звук с телефона.
type Source struct {
	Device string  // адрес телефона; пусто — любой подключённый
	Gain   float64 // громкость относительно звука телефона (множитель); 0 — 1
	Engine *engine.Engine
	Logf   func(string, ...any)

	Arecord string // путь к arecord; пусто — /usr/bin/arecord

	// Dial (необязательный) — подключение к шине; по умолчанию bluez.Open.
	Dial func() (Client, error)
	// OpenPCM (необязательный) — чтение PCM телефона (S16LE); по умолчанию arecord из PCM BlueALSA.
	OpenPCM func(addr string, f audio.Format) (io.ReadCloser, error)
	// Aplay (необязательный) — штатный bluealsa-aplay: Running и Start/Stop службы. nil — не трогать (тесты).
	Aplay Service

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	busUp    bool
	phone    *bluez.PCM // PCM телефона, пока он подключён
	stream   *engine.Stream
	streamOf dbus.ObjectPath
	ended    chan struct{} // поток закончился сам (arecord вышел)
	retryAt  time.Time
	unduck   int
	lastErr  string
	aplayOff bool
}

// Service — штатная служба (bluealsa-aplay).
type Service interface {
	Running() bool
	Start() error
	Stop() error
}

func (s *Source) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// note пишет ошибку в журнал, только если она не повторяет прошлую.
func (s *Source) note(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	s.mu.Lock()
	dup := msg == s.lastErr
	s.lastErr = msg
	s.mu.Unlock()
	if !dup {
		s.logf("%s", msg)
	}
}

// Start останавливает штатный bluealsa-aplay (PCM телефона принимает одного клиента) и начинает следить за телефоном.
func (s *Source) Start(ctx context.Context) error {
	if s.Engine == nil {
		return errors.New("btin: нет движка")
	}
	if s.Aplay != nil {
		if s.Aplay.Running() {
			if err := s.Aplay.Stop(); err != nil {
				return fmt.Errorf("остановить bluealsa-aplay: %w", err)
			}
			s.logf("bluealsa-aplay остановлен: звук телефона теперь идёт через openmediad")
		}
		for end := time.Now().Add(3 * time.Second); s.Aplay.Running(); time.Sleep(50 * time.Millisecond) {
			// скрипт только посылает сигнал: процесс выходит чуть позже
			if time.Now().After(end) {
				return errors.New("bluealsa-aplay не остановился: PCM телефона занят им, звук телефона остаётся штатным")
			}
		}
		s.aplayOff = true
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel, s.done, s.ended = cancel, make(chan struct{}), make(chan struct{}, 1)
	s.mu.Unlock()
	go s.run(ctx)
	return nil
}

// Stop закрывает поток телефона и возвращает штатный bluealsa-aplay (он запускается, даже если его остановил прошлый
// запуск демона, оборванный без Stop: без него звук телефона не играл бы совсем).
func (s *Source) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.stopStream(true)
	if s.Aplay != nil && !s.Aplay.Running() {
		if err := s.Aplay.Start(); err != nil {
			s.logf("запустить bluealsa-aplay не удалось: %v — звук телефона не будет играть до перезагрузки", err)
		} else {
			s.logf("bluealsa-aplay запущен снова: звук телефона играет штатно")
		}
	}
}

func (s *Source) dial() (Client, error) {
	if s.Dial != nil {
		return s.Dial()
	}
	c, err := bluez.Open()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Source) run(ctx context.Context) {
	defer close(s.done)
	backoff := time.Second
	for ctx.Err() == nil {
		c, err := s.dial()
		if err == nil {
			backoff = time.Second
			err = s.serve(ctx, c)
			c.Close()
		}
		s.stopStream(false)
		s.mu.Lock()
		s.busUp, s.phone = false, nil
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		s.note("Bluetooth недоступен: %v; повтор через %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (s *Source) serve(ctx context.Context, c Client) error {
	changes, err := c.Watch(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.busUp = true
	s.mu.Unlock()
	if err := s.refresh(ctx, c); err != nil {
		return err
	}
	for {
		var retry <-chan time.Time // поток оборвался, а телефон играет: открыть заново чуть позже
		s.mu.Lock()
		if !s.retryAt.IsZero() {
			retry = time.After(time.Until(s.retryAt))
		}
		ended := s.ended
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-changes:
			if !ok {
				return fmt.Errorf("соединение с шиной закрыто")
			}
		case <-ended:
		case <-retry:
		}
		if err := s.refresh(ctx, c); err != nil {
			return err
		}
	}
}

// phonePCM — PCM приёма звука A2DP (из него читают) телефона dev (пусто — любого).
func phonePCM(st bluez.State, dev string) (bluez.PCM, bool) {
	for _, p := range st.PCMs {
		if p.A2DP() && !p.Playback() && (dev == "" || strings.EqualFold(p.Address, dev)) {
			return p, true
		}
	}
	return bluez.PCM{}, false
}

// refresh перечитывает BlueALSA: телефон играет — поток движка идёт, перестал или отключился — поток закрыт.
func (s *Source) refresh(ctx context.Context, c Client) error {
	st, err := c.State(ctx)
	if err != nil {
		return err
	}
	p, ok := phonePCM(st, s.Device)
	s.mu.Lock()
	prev := s.phone
	if ok {
		s.phone = &p
	} else {
		s.phone = nil
	}
	s.mu.Unlock()
	switch {
	case ok && prev == nil:
		s.logf("телефон %s подключён: его звук пойдёт через openmediad (%s, %d Гц)", p.Address, p.Codec, p.Sampling)
	case !ok && prev != nil:
		s.logf("телефон %s отключён", prev.Address)
	}
	if ok && p.SoftVolume {
		// платформа приглушила телефон на время диалога: приглушает движок (focus.duck_gain), второй раз не нужно
		if err := c.SetPCMSoftVolume(ctx, p.Path, false); err != nil {
			s.note("громкость PCM телефона: %v", err)
		} else {
			s.mu.Lock()
			s.unduck++
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	streaming, of, wait := s.stream != nil, s.streamOf, s.retryAt
	s.mu.Unlock()
	switch {
	case streaming && (!ok || !p.Running || of != p.Path):
		s.stopStream(false)
	case !streaming && ok && p.Running && (wait.IsZero() || !time.Now().Before(wait)):
		s.startStream(p)
	}
	return nil
}

// startStream запускает поток движка: звук телефона — как музыка (класс music).
func (s *Source) startStream(p bluez.PCM) {
	f := audio.Format{Rate: int(p.Sampling), Channels: int(p.Channels)}
	if f.Rate == 0 || f.Channels == 0 {
		s.note("PCM телефона без формата (%d Гц, %d кан.)", p.Sampling, p.Channels)
		return
	}
	open := s.OpenPCM
	if open == nil {
		open = s.record
	}
	src := &pcmSource{f: f, open: func() (io.ReadCloser, error) { return open(p.Address, f) }}
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	var st *engine.Stream
	done := func(why string) {
		s.mu.Lock()
		mine := s.stream == st
		if mine {
			s.stream, s.streamOf = nil, ""
			s.retryAt = time.Now().Add(2 * time.Second) // не крутиться, если arecord сразу выходит
		}
		s.mu.Unlock()
		if mine {
			s.note("звук телефона прервался (%s)", why)
			select {
			case ended <- struct{}{}:
			default:
			}
		}
	}
	st = s.Engine.NewStream("btin/"+p.Address, engine.Music, src,
		engine.Options{Gain: s.Gain, Spectrum: spectrum.SourceMusic, Endless: true},
		engine.Events{
			Ended:  func() { done("чтение кончилось") },
			Failed: func(err error) { done(err.Error()) },
		})
	s.mu.Lock()
	s.stream, s.streamOf, s.retryAt = st, p.Path, time.Time{}
	s.mu.Unlock()
	st.Play()
	s.logf("телефон %s играет: звук через движок (%d Гц, %d кан.)", p.Address, f.Rate, f.Channels)
}

// stopStream закрывает поток телефона; wait — дождаться, пока вывод действительно остановится (остановка демона).
func (s *Source) stopStream(wait bool) {
	s.mu.Lock()
	st := s.stream
	s.stream, s.streamOf = nil, ""
	s.mu.Unlock()
	if st == nil {
		return
	}
	if wait {
		st.CloseWait()
	} else {
		st.Close()
	}
	s.logf("телефон перестал играть: поток закрыт")
}

// State — снимок для status и отладочной страницы.
type State struct {
	Bus       bool   `json:"bus"`
	Phone     string `json:"phone,omitempty"`
	Codec     string `json:"codec,omitempty"`
	Rate      uint32 `json:"rate,omitempty"`
	Running   bool   `json:"running"`   // телефон шлёт звук
	Streaming bool   `json:"streaming"` // звук идёт через движок
	Unducked  int    `json:"unducked"`  // сколько раз снято приглушение платформы
	AplayOff  bool   `json:"aplay_off"` // штатный bluealsa-aplay остановлен
}

// DebugState — что с телефоном сейчас.
func (s *Source) DebugState() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Bus: s.busUp, Streaming: s.stream != nil, Unducked: s.unduck, AplayOff: s.aplayOff}
	if p := s.phone; p != nil {
		st.Phone, st.Codec, st.Rate, st.Running = p.Address, p.Codec, p.Sampling, p.Running
	}
	return st
}

// record — arecord из PCM BlueALSA телефона (ALSA-плагин выбирает PCM приёма по направлению: запись).
func (s *Source) record(addr string, f audio.Format) (io.ReadCloser, error) {
	r, err := alsa.StartRecord(alsa.Config{Path: s.Arecord, Device: bluez.ALSADevice(addr), Rate: f.Rate, Channels: f.Channels,
		BufferUS: 100 * 1000})
	if err != nil {
		return nil, err
	}
	return recording{r}, nil
}

// recording — arecord как io.ReadCloser: Close останавливает запись (SIGINT).
type recording struct{ *alsa.Recorder }

func (r recording) Close() error { return r.Stop(2 * time.Second) }

// pcmSource — источник движка из PCM телефона. Read отдаёт целые кадры: движок применяет к ним громкость и спектр.
type pcmSource struct {
	f    audio.Format
	open func() (io.ReadCloser, error)

	mu sync.Mutex
	r  io.ReadCloser
}

func (p *pcmSource) Format() audio.Format { return p.f }

func (p *pcmSource) Open(context.Context) (audio.Info, error) {
	r, err := p.open()
	if err != nil {
		return audio.Info{}, err
	}
	p.mu.Lock()
	p.r = r
	p.mu.Unlock()
	return audio.Info{}, nil
}

func (p *pcmSource) Read(b []byte) (int, error) {
	p.mu.Lock()
	r := p.r
	p.mu.Unlock()
	if r == nil {
		return 0, io.EOF
	}
	frame := p.f.FrameBytes()
	n, err := io.ReadFull(r, b[:len(b)-len(b)%frame])
	if err != nil {
		n -= n % frame
		if n > 0 {
			return n, nil
		}
		return 0, io.EOF
	}
	return n, nil
}

func (p *pcmSource) Close() error {
	p.mu.Lock()
	r := p.r
	p.r = nil
	p.mu.Unlock()
	if r != nil {
		return r.Close()
	}
	return nil
}
