package displayspectrum

import (
	"context"
	"sync"
	"time"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/spectrum"
)

// DefaultDelay — на сколько метка времени кадра спектра опережает момент, когда кадр отправлен: через столько после передачи
// звука aplay он прозвучит (труба 64 КБ ≈ 340 мс и буфер смесителя). У штатного mediad кадр уходит сразу, а в поле 1 стоит
// время, когда звук будет слышен: приход − метка ≈ −0,29 с во всех записях штатной музыки и голоса. Метку в прошлом
// (первый вариант: кадр держали 300 мс и ставили время расчёта, приход − метка = +0,30 с) платформа не рисует — анимации
// не было (docs/research/HISTORY.md, §22). Значение оценочное; подбирается глазами настройкой spectrum.delay.
const DefaultDelay = 300 * time.Millisecond

// frameSender — куда уходят сообщения спектра (Client; в тестах подмена).
type frameSender interface{ Send(msg []byte) bool }

// maxHold — дольше этого спектр не удерживают, что бы ни случилось с тем, кто его удерживает: дисплей не должен остаться без спектра насовсем.
var maxHold = 10 * time.Second

// Sender берёт события спектра движка и отправляет их iohub по порядку. Кадр уходит сразу, с меткой «когда прозвучит»
// (время расчёта + delay); остановка потока (6:2) уходит через delay, когда последний кадр уже прозвучал, как у mediad.
//
// Спектр музыки можно ненадолго убрать с дисплея (Hold): платформа рисует его поверх всего ещё 15 секунд после начала каждого трека, и в это время
// на дисплее нельзя показать ничего другого (анимации паузы и перемотки, см. led-ring). Пока спектр удержан, платформе уходит «остановка» потока,
// кадры не отправляются, а новый «старт» ждёт; когда удержание снято, поток объявляется заново, если музыка всё ещё играет.
type Sender struct {
	cli   frameSender
	delay time.Duration
	hub   *journal.Journal // необязательный: журнал о начале и конце потоков спектра

	mu     sync.Mutex
	q      []queued
	wake   chan struct{}
	frames uint64

	live  *liveStream // поток спектра музыки, который сейчас должен рисоваться (после «старта»/«продолжения», до «остановки»)
	off   bool        // платформе сказано «остановлен» из-за удержания (или её «старт» отложен): у неё спектра нет, лишняя «остановка» не нужна
	held  int         // сколько держателей
	epoch int         // номер удержания: освобождение, пришедшее после принудительного снятия, ничего не значит
	timer *time.Timer // принудительное снятие
}

// liveStream — поток спектра музыки, который сейчас должен рисоваться. pending — какое сообщение о нём платформе ещё предстоит отправить после
// удержания («старт» или «продолжение», как объявил его движок); 0 — платформа о нём знает.
type liveStream struct {
	id      uint64
	source  int
	pending int
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
	if ev.Source == SourceMusic && !s.track(ev) {
		s.mu.Unlock()
		return
	}
	s.enqueue(queued{at, ev})
	s.mu.Unlock()
}

// track учитывает событие потока музыки; false — отправлять его не нужно, потому что спектр удержан. Вызывается под замком.
func (s *Sender) track(ev spectrum.Event) bool {
	switch ev.Kind {
	case KindStart, KindResume:
		s.live = &liveStream{id: ev.Stream, source: ev.Source}
		if s.held > 0 {
			s.live.pending, s.off = ev.Kind, true // платформе скажем после удержания
			return false
		}
	case KindStop:
		s.live = nil
		return !s.off // платформа уже остановила спектр сама, а удержание снимется, не найдя потока
	case KindData:
		return s.held == 0
	}
	return true
}

// enqueue кладёт сообщение в очередь и будит отправку. Вызывается под замком.
func (s *Sender) enqueue(q queued) {
	if len(s.q) > 512 { // iohub не принимает: не копить
		s.q = s.q[1:]
	}
	s.q = append(s.q, q)
	s.kick()
}

// kick будит отправку: в очереди что-то изменилось.
func (s *Sender) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Hold убирает спектр музыки с дисплея, пока не вызван release (его можно вызвать любое число раз). Удержаний может быть несколько сразу; спектр
// возвращается, когда сняты все, но не позже чем через maxHold. Если спектр сейчас не рисуется, ничего не меняется, а новый поток спектра
// тоже подождёт.
//
// Hold возвращается, когда платформа уже получила «остановку» (или спустя settle после неё). Остановка, что ждёт в очереди своего часа (delay после
// последнего кадра), торопится: платформа рисует кадры, пока не получит её, и затирала бы первые кадры анимации.
func (s *Sender) Hold() (release func()) {
	s.mu.Lock()
	s.held++
	epoch := s.epoch
	flush := false
	if s.held == 1 {
		s.timer = time.AfterFunc(maxHold, func() { s.unhold(epoch, true) })
		now := time.Now()
		if s.live != nil && !s.off {
			s.off, flush = true, true
			s.enqueue(queued{now, spectrum.Event{Kind: KindStop, Source: s.live.source, Stream: s.live.id, At: now}}) // после кадров, что уже в очереди
			s.log("убран на время анимации", s.live.id)
		}
		for i := range s.q {
			if q := &s.q[i]; q.ev.Kind == KindStop && q.ev.Source == SourceMusic && q.at.After(now) {
				q.at, flush = now, true
			}
		}
		if flush {
			s.kick()
		}
	}
	s.mu.Unlock()
	if flush {
		s.awaitStop()
	}
	var once sync.Once
	return func() { once.Do(func() { s.unhold(epoch, false) }) }
}

// settle — сколько платформе нужно после «остановки», чтобы перестать рисовать спектр. Меньше — анимация ещё может попасть под последний кадр.
const settle = 60 * time.Millisecond

// awaitStop ждёт, пока «остановка» музыки уйдёт из очереди (не дольше 300 мс), и даёт платформе settle.
func (s *Sender) awaitStop() {
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		waiting := false
		for _, q := range s.q {
			waiting = waiting || q.ev.Kind == KindStop && q.ev.Source == SourceMusic
		}
		s.mu.Unlock()
		if !waiting {
			break
		}
	}
	time.Sleep(settle)
}

// unhold снимает одно удержание (all — все сразу, по таймеру). Последнее возвращает поток спектра, если музыка всё ещё играет.
func (s *Sender) unhold(epoch int, all bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch || s.held == 0 {
		return
	}
	s.held--
	if all {
		s.held = 0
	}
	if s.held > 0 {
		return
	}
	s.epoch++
	s.timer.Stop()
	if !s.off {
		return
	}
	s.off = false
	if s.live == nil {
		return
	}
	kind := KindResume // поток платформа уже знает: он только был остановлен
	if s.live.pending != 0 {
		kind, s.live.pending = s.live.pending, 0
	}
	s.enqueue(queued{time.Now(), spectrum.Event{Kind: kind, Source: s.live.source, Stream: s.live.id, At: time.Now()}})
	s.log("возвращён", s.live.id)
}

func (s *Sender) log(what string, stream uint64) {
	if s.hub != nil {
		s.hub.Log(journal.Info, "engine", "визуализация: спектр музыки "+what, "На время анимации паузы или перемотки на дисплее спектр музыки убирают, иначе платформа рисует его поверх анимации.", map[string]any{"stream": stream})
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
			case <-s.wake: // очередь изменилась: срок головы мог наступить раньше
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
