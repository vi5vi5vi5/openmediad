package btout

import (
	"context"
	"time"
)

// reconnect пробует подключить выбранные наушники, если их нет: когда Bluetooth колонки «выключен» (платформа запрещает
// входящие соединения), сами наушники подключиться не могут.
func (s *Sink) reconnect(ctx context.Context, c Client) {
	s.mu.Lock()
	off := s.btOff
	s.mu.Unlock()
	if s.cur.Load() != nil || off {
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
	for _, p := range st.PCMs {
		if p.A2DP() && !p.Playback() {
			// подключён телефон (звук с него на колонку): подключение наушников его выбило бы [проверено 28.10.2026] —
			// телефон и наушники по очереди, ждём, пока он отключится
			return
		}
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
