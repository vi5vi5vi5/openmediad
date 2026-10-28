// Package bluetooth — выход «Bluetooth-наушники»: колонка как источник A2DP. Звук идёт тем же системным aplay, что и на
// динамики, но в PCM BlueALSA (`plug:'bluealsa:DEV=<адрес>,PROFILE=a2dp'`): BlueALSA уже запущена с профилем a2dp-source,
// а её ALSA-плагин есть в прошивке (docs/BUS-PROTOCOL.md, §2.7а). Подключением наушников и громкостью управляет пакет
// system/bluez через системную шину; общий смеситель dmix этот выход не трогает.
//
// Пока наушники подключены, в них открыт один постоянный канал (mixer.go): все потоки движка смешиваются в него, в паузах
// идёт тишина. Платформа принимает любой ставший активным транспорт A2DP за звук телефона, отменяет музыку Яндекса и
// потом считает последним плеером Bluetooth (проверено 28.10.2026, BUS-PROTOCOL.md §2.7б), поэтому транспорт наушников
// ей адресно «удаляется» сразу, как появился, — до того, как канал его включит.
//
// Выход доступен, пока у выбранных наушников есть PCM вывода; за этим следят сигналы BlueZ и BlueALSA (без опроса).
// Отключились наушники — смеситель останавливается с ошибкой, и движок переводит потоки на следующий выход маршрута.
package bluetooth

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/sink/alsa"
	"openmediad/internal/system/bluez"
	"openmediad/internal/system/dbus"
)

// Режимы громкости.
const (
	VolumeSpeaker    = "speaker"    // громкость наушников повторяет громкость колонки (по шкале платформы); меняет BlueALSA
	VolumeHeadphones = "headphones" // громкостью управляют сами наушники, движок её не трогает
)

// Client — то, чем выход пользуется из system/bluez (подменяется в тестах).
type Client interface {
	State(ctx context.Context) (bluez.State, error)
	Watch(ctx context.Context) (<-chan struct{}, error)
	Connect(ctx context.Context, dev dbus.ObjectPath) error
	SetPCMVolume(ctx context.Context, pcm dbus.ObjectPath, level byte) error
	SetPCMSoftVolume(ctx context.Context, pcm dbus.ObjectPath, on bool) error
	Connections(ctx context.Context, pid int) ([]string, error)
	HideTransport(dests []string, path dbus.ObjectPath) error
	ForgetTransport(dests []string, path dbus.ObjectPath) error
	Close()
}

// Master — громкость динамиков колонки (аппаратный Master, им управляют кнопки и Алиса).
type Master interface {
	Level() (cur, max int, ok bool)
	Changes() <-chan struct{} // отметка при каждом изменении (несколько подряд сливаются)
	Close()
}

// Sink — Bluetooth-наушники.
type Sink struct {
	Device    string        // адрес наушников; пусто — любые подключённые
	Aplay     string        // путь к aplay; пусто — /usr/bin/aplay
	Volume    string        // VolumeSpeaker (по умолчанию) или VolumeHeadphones
	VolumeMap VolumeMap     // шкала громкости платформы; пустая — как у bergamot
	Reconnect time.Duration // как часто пробовать подключить выбранные наушники, пока их нет; 0 — не пробовать
	Logf      func(string, ...any)

	// OpenMaster (необязательный) открывает слежение за громкостью колонки при Start; nil — громкость не повторяется.
	OpenMaster func() (Master, error)
	// Dial (необязательный) — подключение к шине; по умолчанию bluez.Open.
	Dial func() (Client, error)
	// OpenOutput (необязательный) — вывод в наушники для смесителя; по умолчанию aplay в PCM BlueALSA.
	OpenOutput func(addr string) (audio.Output, error)
	// PlatformPID (необязательный) — PID платформы (maind --platformd); 0 — не найдена, канал от неё не скрывается.
	PlatformPID func() int
	// Buttons (необязательный) — нажата кнопка плеера на наушниках (buttons.go); nil — кнопки не читаются.
	Buttons func(Button)
	// OpenButtons (необязательный) — устройство кнопок наушников с одним из имён; по умолчанию ищется в /sys/class/input.
	OpenButtons func(names []string) (io.ReadCloser, error)

	cur    atomic.Pointer[target]
	master Master

	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	lastVol   int
	lastErr   string
	busUp     bool
	attempts  int
	mix       *mixer
	mixAddr   string
	hidden    map[dbus.ObjectPath]bool // транспорты наушников, которые платформа уже забыла
	hidePID   int                      // платформа, которой это сказано (перезапустилась — сказать заново)
	hides     int
	keys      io.ReadCloser // устройство кнопок наушников, пока читается
	keysAddr  string
	keysTries int
	arrival   func()
}

// OnArrival задаёт, кого звать, когда наушники подключились и канал в них открыт: звучащие потоки переходят в них.
func (s *Sink) OnArrival(f func()) {
	s.mu.Lock()
	s.arrival = f
	s.mu.Unlock()
}

// target — наушники, в которые сейчас можно играть.
type target struct {
	addr     string
	name     string
	names    []string // имя и псевдоним устройства (по ним ищется устройство кнопок)
	pcm      dbus.ObjectPath
	rate     uint32
	channels byte
}

// Name — имя выхода в маршрутах настроек.
func (s *Sink) Name() string { return "bluetooth" }

// Available — наушники подключены и у них есть PCM вывода. Быстро: читает снимок, который держит Start.
func (s *Sink) Available() bool { return s.cur.Load() != nil }

// Open — поток движка в наушники: через смеситель (он открывается при подключении наушников; если его нет — сейчас).
func (s *Sink) Open(f audio.Format) (audio.Output, error) {
	t := s.cur.Load()
	if t == nil {
		return nil, fmt.Errorf("наушники не подключены")
	}
	m, err := s.ensureMixer(t.addr)
	if err != nil {
		return nil, err
	}
	return m.Open(f)
}

// ensureMixer — работающий смеситель для наушников addr (запускает, если его нет или он остановился).
func (s *Sink) ensureMixer(addr string) (*mixer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mix != nil && s.mixAddr == addr && s.mix.Err() == nil {
		return s.mix, nil
	}
	if s.mix != nil {
		go s.mix.Stop()
		s.mix = nil
	}
	open := s.OpenOutput
	if open == nil {
		aplay := s.Aplay
		open = func(addr string) (audio.Output, error) {
			p, err := alsa.Start(alsa.Config{Path: aplay, Device: bluez.ALSADevice(addr), Rate: 48000, Channels: 2,
				BufferUS: mixBufferUS, PipeBytes: mixPipe})
			if err != nil {
				return nil, err
			}
			return alsa.NewOutput(p), nil
		}
	}
	m, err := startMixer(func() (audio.Output, error) { return open(addr) })
	if err != nil {
		return nil, fmt.Errorf("канал в наушники: %w", err)
	}
	s.mix, s.mixAddr = m, addr
	return m, nil
}

// stopMixer закрывает канал в наушники (наушники отключились или выход останавливается).
func (s *Sink) stopMixer() {
	s.mu.Lock()
	m := s.mix
	s.mix, s.mixAddr = nil, ""
	s.mu.Unlock()
	if m != nil {
		m.Stop()
	}
}

func (s *Sink) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Start начинает следить за наушниками: подключение к шине (с повтором, если шина или BlueZ перезапустились), снимок PCM,
// громкость, переподключение выбранных наушников.
func (s *Sink) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel, s.done = cancel, make(chan struct{})
	s.lastVol = -1
	s.mu.Unlock()
	if s.OpenMaster != nil && s.volumeMode() == VolumeSpeaker {
		m, err := s.OpenMaster()
		if err != nil {
			s.logf("громкость колонки недоступна (%v): громкостью наушников управляют сами наушники", err)
		} else {
			s.master = m
		}
	}
	go s.run(ctx)
	return nil
}

// Stop прекращает слежение. Звучащие потоки не трогает: их закрывает движок.
func (s *Sink) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.stopMixer()
	s.closeButtons()
	if s.master != nil {
		s.master.Close()
	}
}

func (s *Sink) dial() (Client, error) {
	if s.Dial != nil {
		return s.Dial()
	}
	c, err := bluez.Open()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Sink) run(ctx context.Context) {
	defer close(s.done)
	backoff := time.Second
	for ctx.Err() == nil {
		c, err := s.dial()
		if err == nil {
			backoff = time.Second
			err = s.serve(ctx, c)
			c.Close()
		}
		s.setTarget(nil)
		s.stopMixer()
		s.closeButtons()
		s.mu.Lock()
		s.busUp = false
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

// serve обслуживает одно соединение с шиной, пока оно живо.
func (s *Sink) serve(ctx context.Context, c Client) error {
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
	var master <-chan struct{}
	if s.master != nil {
		master = s.master.Changes()
	}
	var retry <-chan time.Time
	var tick *time.Ticker
	if s.Reconnect > 0 && s.Device != "" {
		tick = time.NewTicker(s.Reconnect)
		defer tick.Stop()
		retry = tick.C
	}
	for {
		var mixRetry, keysRetry <-chan time.Time // канал в наушники оборвался, а наушники на месте: открыть заново чуть позже
		if s.cur.Load() != nil && !s.mixerAlive() {
			mixRetry = time.After(3 * time.Second)
		}
		if d := s.buttonsWait(); d > 0 { // кнопок ещё нет: AVRCP подключается позже звука
			keysRetry = time.After(d)
		}
		select {
		case <-mixRetry:
			if err := s.refresh(ctx, c); err != nil {
				return err
			}
		case <-keysRetry:
			s.ensureButtons(s.cur.Load())
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-changes:
			if !ok {
				return fmt.Errorf("соединение с шиной закрыто")
			}
			if err := s.refresh(ctx, c); err != nil {
				return err
			}
		case <-master:
			s.applyVolume(ctx, c, false)
		case <-retry:
			s.reconnect(ctx, c)
		}
	}
}

// refresh перечитывает состояние BlueZ и BlueALSA и обновляет выход.
func (s *Sink) refresh(ctx context.Context, c Client) error {
	st, err := c.State(ctx)
	if err != nil {
		return err
	}
	s.hideTransports(ctx, c, st.Transports) // и без наушников: пропавшие транспорты надо забыть
	p, ok := st.PlaybackPCM(s.Device)
	if !ok {
		s.setTarget(nil)
		return nil
	}
	t := &target{addr: p.Address, pcm: p.Path, rate: p.Sampling, channels: p.Channels}
	if d, ok := st.Device(p.Address); ok {
		t.name, t.names = d.Label(), []string{d.Name, d.Alias}
	}
	fresh := s.setTarget(t)
	if fresh {
		s.applyVolume(ctx, c, true)
	}
	if !s.mixerAlive() {
		if _, err := s.ensureMixer(t.addr); err != nil {
			s.note("%v", err)
		}
	}
	s.ensureButtons(t)
	if fresh {
		s.mu.Lock()
		f := s.arrival
		s.mu.Unlock()
		if f != nil {
			go f()
		}
	}
	return nil
}

func (s *Sink) mixerAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mix != nil && s.mix.Err() == nil
}

// hideTransports прячет от платформы транспорты наушников (BUS-PROTOCOL.md, §2.7б). Иначе она примет включение канала
// за звук телефона: заберёт аудиофокус, отменит музыку и будет считать последним плеером Bluetooth («продолжи»,
// «дальше» уйдут ему). Каждый новый транспорт в наушники платформе адресно «удаляется» сразу, как появился, — до того,
// как канал его включит (refresh зовёт это раньше ensureMixer): тогда она его включения не видит вовсе. Если транспорт
// уже активен (демон перезапустился при открытом канале), сначала «простаивает» — отпустить фокус, если она его забрала.
// Один раз на путь транспорта; платформа перезапустилась — заново.
func (s *Sink) hideTransports(ctx context.Context, c Client, ts []bluez.Transport) {
	var mine []bluez.Transport
	for _, t := range ts {
		if t.ToHeadphones() {
			mine = append(mine, t)
		}
	}
	pid := 0
	if len(mine) > 0 && s.PlatformPID != nil {
		pid = s.PlatformPID()
	}
	var todo []bluez.Transport
	s.mu.Lock()
	if pid != s.hidePID {
		s.hidden, s.hidePID = nil, pid
	}
	hidden := map[dbus.ObjectPath]bool{} // пропавшие транспорты забываются: путь может вернуться с новым подключением
	for _, t := range mine {
		if s.hidden[t.Path] {
			hidden[t.Path] = true
		} else {
			todo = append(todo, t)
		}
	}
	s.hidden = hidden
	s.mu.Unlock()
	if len(todo) == 0 || s.PlatformPID == nil {
		return
	}
	if pid == 0 {
		s.note("платформа не найдена: канал в наушники от неё не скрыт")
		return
	}
	conns, err := c.Connections(ctx, pid)
	if err != nil || len(conns) == 0 {
		s.note("соединения платформы с шиной не найдены (%v): канал в наушники от неё не скрыт", err)
		return
	}
	for _, t := range todo {
		if t.Active() {
			if err := c.HideTransport(conns, t.Path); err != nil {
				s.note("скрыть канал в наушники от платформы: %v", err)
				continue
			}
		}
		if err := c.ForgetTransport(conns, t.Path); err != nil {
			s.note("скрыть канал в наушники от платформы: %v", err)
			continue
		}
		s.mu.Lock()
		s.hidden[t.Path] = true
		s.hides++
		s.mu.Unlock()
		s.logf("канал в наушники скрыт от платформы: она не примет его за телефон и не остановит музыку")
	}
}

// setTarget меняет текущие наушники; true — появились новые (или другой PCM).
func (s *Sink) setTarget(t *target) bool {
	old := s.cur.Swap(t)
	switch {
	case t == nil && old != nil:
		s.logf("наушники %s отключены: выход bluetooth недоступен", old.label())
		go s.stopMixer()
		s.closeButtons()
	case t != nil && (old == nil || old.pcm != t.pcm):
		s.logf("наушники %s подключены (%d Гц, %d кан.): выход bluetooth доступен", t.label(), t.rate, t.channels)
		s.mu.Lock()
		s.lastVol, s.attempts = -1, 0
		s.mu.Unlock()
		return true
	}
	return false
}

func (t *target) label() string {
	if t.name != "" {
		return t.name + " (" + t.addr + ")"
	}
	return t.addr
}

func (s *Sink) volumeMode() string {
	if s.Volume == "" {
		return VolumeSpeaker
	}
	return s.Volume
}

// applyVolume ставит наушникам громкость колонки (режим speaker; пересчёт — volume.go). force — даже если уровень не
// поменялся (новые наушники).
func (s *Sink) applyVolume(ctx context.Context, c Client, force bool) {
	t := s.cur.Load()
	if t == nil || s.master == nil {
		return
	}
	cur, max, ok := s.master.Level()
	if !ok {
		return
	}
	lvl := int(s.VolumeMap.Headphones(cur))
	s.mu.Lock()
	same := s.lastVol == lvl
	s.lastVol = lvl
	s.mu.Unlock()
	if same && !force {
		return
	}
	if force {
		// громкость меняет сама BlueALSA: так она работает с любыми наушниками и не спорит с их кнопками по AVRCP
		if err := c.SetPCMSoftVolume(ctx, t.pcm, true); err != nil {
			s.note("громкость наушников: %v", err)
			return
		}
	}
	if err := c.SetPCMVolume(ctx, t.pcm, byte(lvl)); err != nil {
		s.note("громкость наушников: %v", err)
		return
	}
	s.logf("громкость наушников %d/127 (колонка %d из %d, уровень %d из %d)", lvl, cur, max, s.VolumeMap.Level(cur), s.VolumeMap.Levels())
}

// reconnect пробует подключить выбранные наушники, если их нет: когда Bluetooth колонки «выключен» (платформа запрещает
// входящие соединения), сами наушники подключиться не могут.
func (s *Sink) reconnect(ctx context.Context, c Client) {
	if s.cur.Load() != nil {
		return
	}
	st, err := c.State(ctx)
	if err != nil {
		return
	}
	d, ok := st.Device(s.Device)
	if !ok || !d.Paired || d.Connected {
		return
	}
	s.mu.Lock()
	s.attempts++
	n := s.attempts
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.Connect(cctx, d.Path); err != nil {
		if n == 1 || n%20 == 0 { // наушники выключены или в футляре — это обычное дело, журнал не засоряем
			s.note("подключить наушники %s не удалось (попытка %d): %v", d.Label(), n, err)
		}
		return
	}
	s.logf("наушники %s подключены по инициативе колонки", d.Label())
}

// note пишет ошибку в журнал, только если она не повторяет прошлую.
func (s *Sink) note(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	s.mu.Lock()
	dup := msg == s.lastErr
	s.lastErr = msg
	s.mu.Unlock()
	if !dup {
		s.logf("%s", msg)
	}
}

// State — снимок для status и отладочной страницы.
type State struct {
	Bus       bool   `json:"bus"`
	Connected bool   `json:"connected"`
	Device    string `json:"device,omitempty"`
	Name      string `json:"name,omitempty"`
	Rate      uint32 `json:"rate,omitempty"`
	Volume    int    `json:"volume_127"`
	Want      string `json:"want,omitempty"`
	Channel   bool   `json:"channel"`         // постоянный канал (смеситель) открыт
	Streams   int    `json:"streams"`         // сколько потоков движка звучит в наушниках
	Hidden    int    `json:"hidden_platform"` // сколько транспортов наушников скрыто от платформы
	Buttons   bool   `json:"buttons"`         // кнопки наушников читаются
}

// DebugState — что с наушниками сейчас.
func (s *Sink) DebugState() any {
	s.mu.Lock()
	st := State{Bus: s.busUp, Volume: s.lastVol, Want: strings.ToUpper(s.Device), Hidden: s.hides, Buttons: s.keys != nil}
	m := s.mix
	s.mu.Unlock()
	if m != nil && m.Err() == nil {
		st.Channel, st.Streams = true, m.Streams()
	}
	if t := s.cur.Load(); t != nil {
		st.Connected, st.Device, st.Name, st.Rate = true, t.addr, t.name, t.rate
	}
	return st
}
