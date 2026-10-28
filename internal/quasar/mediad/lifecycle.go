package mediad

import "time"

// Общее для трёх видов плееров: таймеры плеера и возврат команд настоящему mediad.

// timers — таймеры одного плеера: прогресс, flag, отложенные события. Все отменяются разом, когда плеер закончился, встал на
// паузу или отдан mediad. Защищается m.mu.
type timers struct{ cancel []func() }

// active — есть ли запущенные таймеры.
func (t *timers) active() bool { return t.cancel != nil }

// stop отменяет все таймеры. Вызывать под m.mu.
func (t *timers) stop() {
	for _, c := range t.cancel {
		c()
	}
	t.cancel = nil
}

// schedule запускает f через d и запоминает отмену в t. alive (необязательная, вызывается под m.mu) говорит, жив ли ещё
// плеер: если его успели снять между вызовами (release пришёл раньше), таймер сразу отменяется. m.mu держать не нужно.
func (m *Machine) schedule(t *timers, alive func() bool, d time.Duration, f func()) {
	cancel := m.After(d, f)
	m.mu.Lock()
	if alive == nil || alive() {
		t.cancel = append(t.cancel, cancel)
		cancel = nil
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// forward отдаёт команды настоящему mediad по порядку (откат). Вызывать без m.mu.
func (m *Machine) forward(ctx any, cmds ...[]byte) {
	if m.Forward == nil {
		return
	}
	for _, c := range cmds {
		if c != nil {
			m.Forward(ctx, c)
		}
	}
}
