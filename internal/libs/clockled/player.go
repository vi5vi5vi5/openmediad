package clockled

import (
	"sync"
	"time"
)

// Player проигрывает анимации на дисплее, пока им пользуется прошивка. Что для этого нужно:
//   - запомнить, что горело до анимации (обычно время), и вернуть это в конце: сама прошивка нарисует время только при смене минуты;
//   - перед каждым кадром убедиться, что на дисплее то, что мы записали сами. Если там другое, прошивка нарисовала своё (спектр, время, анимацию
//     Алисы), и мы уступаем и ничего не возвращаем: чужое важнее нашего.
//
// Новая анимация, пришедшая во время старой, заменяет её; «что было до» при этом остаётся тем, что было до первой.
type Player struct {
	Dev  Device
	Logf func(string, ...any)
	Tick time.Duration    // как часто обновлять кадр (по умолчанию 20 мс: кадры прошивки по 20 мс)
	Now  func() time.Time // часы (по умолчанию time.Now)

	mu      sync.Mutex
	job     *job
	running bool
	closed  bool
	wake    chan struct{}
	stop    chan struct{}
	wg      sync.WaitGroup
}

type job struct {
	anim  *Anim
	scale uint8
	start time.Time
	tag   string
}

func (p *Player) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Player) logf(f string, a ...any) {
	if p.Logf != nil {
		p.Logf(f, a...)
	}
}

func (p *Player) tick() time.Duration {
	if p.Tick > 0 {
		return p.Tick
	}
	return 20 * time.Millisecond
}

// Play начинает анимацию (или заменяет играющую) с яркостью scale из 255. Не блокирует. tag — для журнала.
func (p *Player) Play(a *Anim, scale uint8, tag string) {
	if a == nil || len(a.Steps) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.job = &job{anim: a, scale: scale, start: p.now(), tag: tag}
	if p.running {
		select {
		case p.wake <- struct{}{}:
		default:
		}
		return
	}
	if p.wake == nil {
		p.wake, p.stop = make(chan struct{}, 1), make(chan struct{})
	}
	p.running = true
	p.wg.Add(1)
	go p.run()
}

// Active — анимация сейчас играет.
func (p *Player) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Close останавливает анимацию (возвращает то, что было до неё) и ждёт завершения.
func (p *Player) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		if p.stop != nil {
			close(p.stop)
		}
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Player) run() {
	defer p.wg.Done()
	var base, last Frame
	var haveBase, wrote bool
	// restore возвращает то, что было до анимации, если на дисплее всё ещё наш последний кадр.
	restore := func() {
		if !wrote || !haveBase {
			return
		}
		if cur, err := p.Dev.Read(); err == nil && cur == last {
			if err := p.Dev.Write(base); err != nil {
				p.logf("дисплей: вернуть прежнее не вышло: %v", err)
			}
			last = base
		}
	}
	end := func() { // конец работы: пока ждали, мог прийти новый заказ — об этом знает только замок
		p.running = false
		p.job = nil
		p.mu.Unlock()
	}
	t := time.NewTicker(p.tick())
	defer t.Stop()
	for {
		p.mu.Lock()
		j := p.job
		p.mu.Unlock()
		if j == nil {
			p.mu.Lock()
			end()
			return
		}
		el := p.now().Sub(j.start)
		if el >= j.anim.Lit() {
			restore()
			p.mu.Lock()
			if p.job != j { // заменили, пока возвращали: играем новую
				p.mu.Unlock()
				continue
			}
			end()
			return
		}
		f, _ := j.anim.At(el)
		f = f.Scale(j.scale)
		cur, err := p.Dev.Read()
		if err != nil {
			p.logf("дисплей: чтение: %v", err)
			p.mu.Lock()
			end()
			return
		}
		switch {
		case !haveBase:
			base, haveBase = cur, true
		case wrote && cur != last:
			p.logf("дисплей: прошивка нарисовала своё поверх анимации «%s», уступаю", j.tag)
			p.mu.Lock()
			end()
			return
		}
		if f != cur {
			if err := p.Dev.Write(f); err != nil {
				p.logf("дисплей: запись: %v", err)
				p.mu.Lock()
				end()
				return
			}
		}
		last, wrote = f, true
		select {
		case <-t.C:
		case <-p.wake:
		case <-p.stop:
			restore()
			p.mu.Lock()
			end()
			return
		}
	}
}
