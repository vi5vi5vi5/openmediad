package iohub

import (
	"context"
	"sync"
	"time"

	"openmediad/internal/journal"
	"openmediad/internal/spectrum"
)

// DefaultDelay — на сколько метка времени кадра спектра опережает момент, когда кадр отправлен: через столько после передачи
// звука aplay он прозвучит (труба 64 КБ ≈ 340 мс и буфер смесителя). У штатного mediad кадр уходит сразу, а в поле 1 стоит
// время, когда звук будет слышен: приход − метка ≈ −0,29 с во всех записях штатной музыки и голоса. Метку в прошлом
// (первый вариант: кадр держали 300 мс и ставили время расчёта, приход − метка = +0,30 с) платформа не рисует — анимации
// не было (docs/research/HISTORY.md, §22). Значение оценочное; подбирается глазами настройкой spectrum.delay.
const DefaultDelay = 300 * time.Millisecond

// frameSender — куда уходят сообщения спектра (Client; в тестах подмена).
type frameSender interface{ Send(msg []byte) bool }

// Sender берёт события спектра движка и отправляет их iohub по порядку. Кадр уходит сразу, с меткой «когда прозвучит»
// (время расчёта + delay); остановка потока (6:2) уходит через delay, когда последний кадр уже прозвучал, как у mediad.
type Sender struct {
	cli   frameSender
	delay time.Duration
	hub   *journal.Journal // необязательный: журнал о начале и конце потоков спектра

	mu     sync.Mutex
	q      []queued
	wake   chan struct{}
	frames uint64
}

type queued struct {
	at time.Time // когда отправить (не раньше; порядок очереди сохраняется)
	ev spectrum.Event
}

// NewSender создаёт отправителя спектра поверх клиента iohub.
func NewSender(cli frameSender, delay time.Duration, hub *journal.Journal) *Sender {
	return &Sender{cli: cli, delay: delay, hub: hub, wake: make(chan struct{}, 1)}
}

// Add ставит событие в очередь (копирует кадр: движок переиспользует срез).
func (s *Sender) Add(ev spectrum.Event) {
	if ev.DB != nil {
		ev.DB = append([]float32(nil), ev.DB...)
	}
	at := time.Now()
	switch ev.Kind {
	case KindData:
		ev.At = ev.At.Add(s.delay) // метка кадра — момент, когда звук прозвучит; сам кадр уходит сразу
	case KindStop:
		at = at.Add(s.delay) // остановка — после того, как прозвучал последний кадр
	}
	s.mu.Lock()
	if len(s.q) > 512 { // iohub не принимает: не копить
		s.q = s.q[1:]
	}
	s.q = append(s.q, queued{at, ev})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sender) Run(ctx context.Context) {
	for {
		s.mu.Lock()
		var head *queued
		if len(s.q) > 0 {
			head = &s.q[0]
		}
		var wait time.Duration
		if head != nil {
			wait = time.Until(head.at)
		}
		s.mu.Unlock()
		switch {
		case head == nil:
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
		case wait > 0:
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		default:
			s.mu.Lock()
			q := s.q[0]
			s.q = s.q[1:]
			s.mu.Unlock()
			s.send(q.ev)
		}
	}
}

func (s *Sender) send(ev spectrum.Event) {
	switch ev.Kind {
	case KindData:
		s.frames++
		s.cli.Send(Data(uint64(ev.At.UnixNano()), ev.DB, ev.Stream, uint64(ev.Source)))
	default:
		s.cli.Send(Control(uint64(ev.Kind), ev.Stream, uint64(ev.Source)))
		if s.hub != nil {
			names := map[int]string{KindStart: "начат", KindStop: "остановлен", KindResume: "продолжен"}
			s.hub.Log(journal.Info, "engine", "визуализация: поток спектра "+names[ev.Kind], "Для дисплея и светодиодов: сервису iohub отправлено служебное сообщение потока спектра (как это делает mediad). Кадры спектра идут 25 раз в секунду и в журнал не пишутся.",
				map[string]any{"stream": ev.Stream, "кадров_отправлено": s.frames})
		}
	}
}
