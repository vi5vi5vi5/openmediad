package btout

import (
	"context"
	"strings"
)

// Громкость динамиков, пока подключены наушники (HoldSpeaker). Уровень у платформы один: кнопки, голос и колесо наушников
// меняют его, она ставит его наушникам (hideTransports) и Master колонки, — и голос Алисы на динамиках звучал бы так же
// громко или тихо, как музыка в наушниках. Поэтому, пока платформа ведёт громкость наушников, Master держится на значении
// до подключения: её изменение сразу возвращается (уровень на экране и в наушниках остаётся её). Наушники отключились —
// уровень платформы ставится обратно, как голосом («громкость N», PlatformLevel): иначе первое же нажатие кнопки поставило
// бы динамикам уровень наушников. Голосом платформа ставит только чётные уровни (0–10 ×2), поэтому нечётный уровень
// динамиков возвращается на шаг тише. Через транспорт отключившихся наушников уровень не передать: платформа идёт к
// объекту BlueZ, а его уже нет (UnknownObject, проверено 28.10.2026).

// holdSpeakerAt запоминает громкость динамиков при подключении наушников t.
func (s *Sink) holdSpeakerAt(t *target) {
	if !s.HoldSpeaker || s.master == nil || s.volumeMode() != VolumeSpeaker {
		return
	}
	cur, _, ok := s.master.Level()
	if !ok {
		return
	}
	s.mu.Lock()
	already := s.held >= 0
	if !already {
		s.held, s.heldWant = cur, cur
	}
	s.mu.Unlock()
	if !already {
		s.logf("громкость динамиков удерживается (уровень %d из %d), пока подключены наушники: кнопки и голос меняют громкость наушников",
			s.VolumeMap.Level(cur), s.VolumeMap.Levels())
	}
}

// holdSpeaker — изменение Master при удержании: если платформа поставила своё (уровень наушников), вернуть громкость
// динамиков. false — громкость не удерживается (обычная обработка: applyVolume).
func (s *Sink) holdSpeaker() bool {
	t := s.cur.Load()
	s.mu.Lock()
	held := s.held
	synced := t != nil && s.synced[strings.ToUpper(t.addr)]
	s.mu.Unlock()
	if held < 0 || s.master == nil {
		return false
	}
	if !synced { // громкость наушников ведёт движок (платформы нет или транспорт ещё спрятан): как раньше
		return false
	}
	cur, _, ok := s.master.Level()
	if !ok || cur == held {
		return true
	}
	s.mu.Lock()
	s.heldWant = cur
	s.mu.Unlock()
	if err := s.master.Set(held); err != nil {
		s.note("удержать громкость динамиков: %v", err)
	}
	return true
}

// releaseSpeaker — наушники отключились: удержание снимается, а уровень платформы возвращается к уровню динамиков.
func (s *Sink) releaseSpeaker() {
	s.mu.Lock()
	held, want := s.held, s.heldWant
	s.held = -1
	s.mu.Unlock()
	if held < 0 || want == held || s.PlatformLevel == nil {
		return
	}
	lvl := s.VolumeMap.Level(held)
	go func() { // текстовый запрос — через облако, сотни миллисекунд: serve не ждёт
		if err := s.PlatformLevel(lvl); err != nil {
			s.note("вернуть платформе уровень динамиков: %v", err)
			return
		}
		s.logf("наушники отключены: уровень громкости снова у динамиков (%d из %d)", lvl, s.VolumeMap.Levels())
	}()
}

// platformBluetooth выполняет «выключи / включи блютуз» (PlatformBluetooth).
func (s *Sink) platformBluetooth(ctx context.Context, c Client, on bool) {
	s.mu.Lock()
	s.btOff = !on
	s.attempts = 0
	s.mu.Unlock()
	if on {
		s.logf("«включи блютуз»: наушники снова подключаются сами")
		s.reconnect(ctx, c)
		return
	}
	t := s.cur.Load()
	if t == nil {
		s.logf("«выключи блютуз»: наушники не подключены и сами подключаться не будут до «включи блютуз»")
		return
	}
	if err := c.Disconnect(ctx, t.dev); err != nil {
		s.note("«выключи блютуз»: отключить наушники %s: %v", t.label(), err)
		return
	}
	s.logf("«выключи блютуз»: наушники %s отключены и сами подключаться не будут до «включи блютуз»", t.label())
}
