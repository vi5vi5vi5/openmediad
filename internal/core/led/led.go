// Package led — общая «панель индикации»: кто играет и куда идёт звук.
//
// Модули сообщают панели, что они делают, а не как это показать: источник музыки («yandex-music», «spotify-soloist») — роль Source, выход
// звука («bt-out», «speaker») — роль Output. Как это нарисовать (цвет, яркость, какой светодиод), решает компонент подсветки, который следит за панелью
// (Watch). Нет компонента подсветки — панель просто хранит состояние, модулям проверять ничего не нужно.
package led

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Color — цвет светодиода.
type Color struct{ R, G, B uint8 }

// IsZero — цвета нет (светодиод не горит).
func (c Color) IsZero() bool { return c == Color{} }

// Hex — запись #rrggbb.
func (c Color) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// Scale — цвет при яркости b (0…255).
func (c Color) Scale(b uint8) Color {
	k := uint16(b)
	return Color{uint8(uint16(c.R) * k / 255), uint8(uint16(c.G) * k / 255), uint8(uint16(c.B) * k / 255)}
}

// ParseColor разбирает цвет вида #rrggbb (решётка необязательна).
func ParseColor(s string) (Color, error) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) != 6 {
		return Color{}, fmt.Errorf("цвет %q: нужен формат #rrggbb", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return Color{}, fmt.Errorf("цвет %q: нужен формат #rrggbb", s)
	}
	return Color{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
}

// Role — что именно показывает светодиод.
type Role int

const (
	Source Role = iota // кто играет музыку (Яндекс, Spotify …)
	Output             // куда она идёт (динамики, наушники …)
)

func (r Role) String() string {
	if r == Output {
		return "вывод"
	}
	return "источник"
}

// Indicator — то, чем пользуются модули: сказать «я играю» и «я закончил». Вызовы дешёвые, из любых горутин, повторные безвредны.
// def — цвет, который модуль предлагает для себя (нулевой — без предложения); пользователь может задать свой в настройках подсветки.
type Indicator interface {
	On(role Role, name string, def Color)
	Off(role Role, name string)
}

// Entry — один включённый модуль.
type Entry struct {
	Name    string
	Default Color
	Since   time.Time
}

// State — что сейчас надо показать: по роли самый недавний из включённых; nil — ничего.
type State struct{ Source, Output *Entry }

// Panel — реализация Indicator, которую держит демон. Без наблюдателей ничего не делает, кроме учёта.
type Panel struct {
	mu       sync.Mutex
	entries  [2][]Entry
	watchers map[int]func(State)
	next     int
	now      func() time.Time
}

// NewPanel создаёт пустую панель.
func NewPanel() *Panel { return &Panel{watchers: map[int]func(State){}, now: time.Now} }

var _ Indicator = (*Panel)(nil)

// On включает запись (name, role); повторный вызов обновляет предложенный цвет и не меняет порядок.
func (p *Panel) On(role Role, name string, def Color) {
	if name == "" || role < Source || role > Output {
		return
	}
	p.mu.Lock()
	before := p.stateLocked()
	list := p.entries[role]
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].Default, found = def, true
		}
	}
	if !found {
		p.entries[role] = append(list, Entry{Name: name, Default: def, Since: p.now()})
	}
	p.finish(before)
}

// Off выключает запись; такой не было — ничего не происходит.
func (p *Panel) Off(role Role, name string) {
	if role < Source || role > Output {
		return
	}
	p.mu.Lock()
	before := p.stateLocked()
	list := p.entries[role]
	for i := range list {
		if list[i].Name == name {
			p.entries[role] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	p.finish(before)
}

// State — что показывать сейчас.
func (p *Panel) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked()
}

func (p *Panel) stateLocked() State {
	var s State
	if l := p.entries[Source]; len(l) > 0 {
		e := l[len(l)-1]
		s.Source = &e
	}
	if l := p.entries[Output]; len(l) > 0 {
		e := l[len(l)-1]
		s.Output = &e
	}
	return s
}

// finish вызывается с взятым замком: сравнивает состояние до и после, отпускает замок и при изменении будит наблюдателей.
func (p *Panel) finish(before State) {
	after := p.stateLocked()
	var fs []func(State)
	if !same(before.Source, after.Source) || !same(before.Output, after.Output) {
		for _, f := range p.watchers {
			fs = append(fs, f)
		}
	}
	p.mu.Unlock()
	for _, f := range fs {
		f(after)
	}
}

func same(a, b *Entry) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && a.Default == b.Default
}

// Watch подписывает f на изменения и сразу вызывает его с текущим состоянием. f вызывается из горутины, изменившей панель, без замков:
// он должен быстро возвращаться (запомнить и разбудить свою горутину). stop отписывает.
func (p *Panel) Watch(f func(State)) (stop func()) {
	p.mu.Lock()
	id := p.next
	p.next++
	p.watchers[id] = f
	cur := p.stateLocked()
	p.mu.Unlock()
	f(cur)
	return func() {
		p.mu.Lock()
		delete(p.watchers, id)
		p.mu.Unlock()
	}
}
