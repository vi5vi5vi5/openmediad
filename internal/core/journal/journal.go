// Package journal — журнал демона: кольцо последних записей в памяти, подписка на новые записи и (по желанию) копия
// строк в поток вывода.
//
// Журнал нужен человеку, который пользуется движком или развивает его: что произошло, почему и что сделал движок.
// Пишут все компоненты через Log или Logf. Секреты (ключи потоков, подписанные ссылки, тела директив) в журнал не
// попадают: компоненты передают только безопасные поля (см. internal/core/quasar/explain).
package journal

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Уровни записи по возрастанию важности.
const (
	Trace = "trace" // фон: прогресс плеера раз в секунду, служебные кадры
	Info  = "info"
	Warn  = "warn"
	Error = "error"
)

// Rank — порядковый номер уровня (неизвестный уровень считается info).
func Rank(lvl string) int {
	switch lvl {
	case Trace:
		return 0
	case Warn:
		return 2
	case Error:
		return 3
	}
	return 1
}

// ValidLevel — допустимое ли имя уровня.
func ValidLevel(lvl string) bool {
	switch lvl {
	case Trace, Info, Warn, Error:
		return true
	}
	return false
}

// Entry — одна запись журнала.
type Entry struct {
	ID   uint64         `json:"id"`
	T    int64          `json:"t"`              // Unix, миллисекунды
	Lvl  string         `json:"lvl"`            // trace | info | warn | error
	Src  string         `json:"src"`            // откуда: shim, player, music, voice, sound, aliced, system…
	Msg  string         `json:"msg"`            // коротко, что произошло
	Why  string         `json:"why,omitempty"`  // пояснение простыми словами: что это значит и что сделал движок
	Data map[string]any `json:"data,omitempty"` // детали без секретов
}

// Line — запись одной строкой для вывода в консоль и в файл.
func (e Entry) Line() string {
	var b strings.Builder
	b.WriteString(time.UnixMilli(e.T).Format("15:04:05.000"))
	fmt.Fprintf(&b, " %-5s %-11s %s", e.Lvl, e.Src, e.Msg)
	if len(e.Data) > 0 {
		keys := make([]string, 0, len(e.Data))
		for k := range e.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%v", k, e.Data[k])
		}
	}
	return b.String()
}

// Journal — кольцевой журнал на последние Cap записей и подписчики на новые. Нулевое значение готово к работе.
type Journal struct {
	Cap   int       // сколько записей держать (по умолчанию 2000)
	Out   io.Writer // если задан, каждая сохранённая запись пишется сюда строкой (консоль при `run`, файл журнала)
	Start time.Time

	mu     sync.Mutex
	min    int // записи ниже этого уровня не сохраняются
	ring   []Entry
	head   int // индекс самой старой записи, когда кольцо полно
	next   uint64
	subs   map[chan Entry]struct{}
	counts map[string]uint64 // записей по источникам за всё время
}

func (j *Journal) init() {
	if j.ring != nil {
		return
	}
	if j.Cap <= 0 {
		j.Cap = 2000
	}
	j.ring = make([]Entry, 0, min(j.Cap, 256))
	j.subs = map[chan Entry]struct{}{}
	j.counts = map[string]uint64{}
	if j.min == 0 {
		j.min = Rank(Info)
	}
	if j.Start.IsZero() {
		j.Start = time.Now()
	}
}

// SetLevel задаёт наименьший сохраняемый уровень (по умолчанию info: фон trace не вытесняет важное из кольца).
func (j *Journal) SetLevel(lvl string) {
	j.mu.Lock()
	j.init()
	j.min = Rank(lvl)
	j.mu.Unlock()
}

// Level — текущий наименьший сохраняемый уровень.
func (j *Journal) Level() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.init()
	return [...]string{Trace, Info, Warn, Error}[j.min]
}

// Log добавляет запись и рассылает её подписчикам. Медленный подписчик пропускает записи, а не тормозит движок.
func (j *Journal) Log(lvl, src, msg, why string, data map[string]any) {
	j.mu.Lock()
	j.init()
	if Rank(lvl) < j.min {
		j.mu.Unlock()
		return
	}
	j.next++
	e := Entry{ID: j.next, T: time.Now().UnixMilli(), Lvl: lvl, Src: src, Msg: msg, Why: why, Data: data}
	if len(j.ring) < j.Cap {
		j.ring = append(j.ring, e)
	} else {
		j.ring[j.head] = e
		j.head = (j.head + 1) % j.Cap
	}
	j.counts[src]++
	subs := make([]chan Entry, 0, len(j.subs))
	for c := range j.subs {
		subs = append(subs, c)
	}
	if j.Out != nil {
		io.WriteString(j.Out, e.Line()+"\n")
	}
	j.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- e:
		default: // подписчик не успевает: запись ему не достанется, он увидит разрыв по номерам ID
		}
	}
}

// Add добавляет готовую запись (время и номер ставит журнал).
func (j *Journal) Add(e Entry) { j.Log(e.Lvl, e.Src, e.Msg, e.Why, e.Data) }

// Logf — функция журнала уровня info для источника src (без пояснения): так пишут компоненты, которым не нужны уровни.
func (j *Journal) Logf(src string) func(string, ...any) {
	return func(format string, a ...any) {
		j.Log(Info, src, fmt.Sprintf(format, a...), "", nil)
	}
}

// Since возвращает записи с ID больше after (по порядку), не более limit самых свежих (0 — все).
func (j *Journal) Since(after uint64, limit int) []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.init()
	n := len(j.ring)
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		e := j.ring[(j.head+i)%n]
		if e.ID > after {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Subscribe возвращает канал новых записей и функцию отписки.
func (j *Journal) Subscribe() (<-chan Entry, func()) {
	j.mu.Lock()
	j.init()
	c := make(chan Entry, 256)
	j.subs[c] = struct{}{}
	j.mu.Unlock()
	return c, func() {
		j.mu.Lock()
		delete(j.subs, c)
		j.mu.Unlock()
	}
}

// Subscribers — число подключённых слушателей.
func (j *Journal) Subscribers() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.subs)
}

// Counts — сколько записей накоплено по источникам (для страницы состояния).
func (j *Journal) Counts() map[string]uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.init()
	out := make(map[string]uint64, len(j.counts))
	for k, v := range j.counts {
		out[k] = v
	}
	return out
}
