package daemon

import (
	"sort"
	"sync"
	"time"

	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/wire"
)

// Учёт работы штатного mediad. Движок берёт себе только то, что умеет; остальное играет штатный плеер, который работает рядом. Чтобы однажды
// обойтись без него (режим replace, PLAN.md §10.4), нужно знать, что именно он ещё делает на этой колонке: какие команды платформы до него
// доходят и что он играет сам. Счётчики показывает `openmediad status`: пока в «сыграл сам» что-то появляется, без штатного плеера звук пропадёт.

// stockUse считает команды, ушедшие штатному mediad, и то, что он сыграл сам.
type stockUse struct {
	mu     sync.Mutex
	since  time.Time
	cmds   map[string]*stockCount // команда платформы, которую движок пропустил: «play (музыка и потоки)»
	backs  map[string]*stockCount // команда, которую движок взял, но вернул (ошибка до первого звука)
	played map[string]*stockCount // штатный mediad сам начал играть: по виду плеера
}

type stockCount struct {
	n    uint64
	last time.Time
}

func newStockUse() *stockUse {
	return &stockUse{since: time.Now(), cmds: map[string]*stockCount{}, backs: map[string]*stockCount{}, played: map[string]*stockCount{}}
}

func (s *stockUse) add(m map[string]*stockCount, key string) {
	s.mu.Lock()
	c := m[key]
	if c == nil {
		c = &stockCount{}
		m[key] = c
	}
	c.n++
	c.last = time.Now()
	s.mu.Unlock()
}

// playerName — вид плеера простыми словами.
func playerName(k msg.PlayerKind) string {
	switch k {
	case msg.PlayerAudio:
		return "музыка и потоки"
	case msg.PlayerVoice:
		return "голос Алисы"
	case msg.PlayerFile:
		return "файлы звуков"
	}
	return "неизвестный плеер"
}

func commandKey(c msg.AudioCommand) string {
	k := c.Classify()
	if k == msg.CmdFocus || k == msg.CmdEqualizer {
		return k.String() // не про конкретный плеер
	}
	return k.String() + " (" + playerName(c.Player().Kind) + ")"
}

// passed — команда платформы ушла штатному mediad как есть.
func (s *stockUse) passed(c msg.AudioCommand) { s.add(s.cmds, commandKey(c)) }

// returned — движок взял команду, но не смог и вернул её штатному mediad.
func (s *stockUse) returned(body []byte) { s.add(s.backs, commandKey(msg.AudioCommand(body))) }

// fromStock смотрит кадр, который штатный mediad отправил платформе: событие «играет» значит, что он заиграл сам.
func (s *stockUse) fromStock(frame []byte) {
	m, err := wire.Unwrap(frame)
	if err != nil || m.Type != wire.TypeAudioEvent {
		return
	}
	ev := msg.AudioEvent(m.Body)
	if ev.Kind() == 1 && ev.State() == 3 { // смена состояния: PLAYING
		s.add(s.played, playerName(ev.Player().Kind))
	}
}

func counts(m map[string]*stockCount, now time.Time) []ctlsock.StockCount {
	out := make([]ctlsock.StockCount, 0, len(m))
	for k, c := range m {
		out = append(out, ctlsock.StockCount{What: k, N: c.n, AgoS: int64(now.Sub(c.last).Seconds())})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].What < out[j].What
	})
	return out
}

func (s *stockUse) snapshot() *ctlsock.StockUse {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return &ctlsock.StockUse{SinceS: int64(now.Sub(s.since).Seconds()), Played: counts(s.played, now), Returned: counts(s.backs, now),
		Passed: counts(s.cmds, now)}
}
