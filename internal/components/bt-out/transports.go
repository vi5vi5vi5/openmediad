package btout

import (
	"context"
	"strings"

	"openmediad/internal/libs/bluez"
	"openmediad/internal/libs/dbus"
)

// Скрытие канала в наушники от платформы (BUS-PROTOCOL.md, §2.7б).

// platformView — что платформа знает о транспорте наушников (с наших слов).
type platformView int

const (
	viewOwn       platformView = iota // ничего не говорили: она видит его таким, как его объявил BlueZ
	viewForgotten                     // сказано «удалён»: его включения она не видит
	viewIdle                          // возвращён простаивающим: видит громкость наушников, но не их звук
)

// hideTransports прячет от платформы звук в наушники (BUS-PROTOCOL.md, §2.7б). Иначе она примет включение канала за звук
// телефона: заберёт аудиофокус, отменит музыку и будет считать последним плеером Bluetooth («продолжи», «дальше» уйдут
// ему). Громкость наушников она тоже ведёт через объект транспорта: ставит им громкость своего уровня, берёт себе их
// громкость (колесо, кнопки), показывает её на экране. Поэтому по каждому транспорту в наушники:
//   - простаивает — адресно «удалён» (до того, как канал его включит: refresh зовёт это раньше ensureMixer); тогда его
//     включения платформа не видит;
//   - включился — в режиме громкости speaker адресно «появился», но простаивающим: дальше, пока канал открыт, BlueZ
//     состояние не меняет, и платформа видит только громкость; в режиме headphones остаётся «удалён» (громкость
//     наушников — своя);
//   - активен, а мы о нём ещё ничего не говорили (демон перезапустился при открытом канале, перезапустилась платформа) —
//     сначала «простаивает»: она отпустит фокус, если забрала.
func (s *Sink) hideTransports(ctx context.Context, c Client, ts []bluez.Transport) {
	var mine []bluez.Transport
	for _, t := range ts {
		if t.ToHeadphones() {
			mine = append(mine, t)
		}
	}
	defer func() { // чьей громкостью платформа сейчас управляет сама
		s.mu.Lock()
		s.synced = map[string]bool{}
		for _, t := range mine {
			if s.view[t.Path] == viewIdle {
				s.synced[strings.ToUpper(t.Address)] = true
			}
		}
		s.mu.Unlock()
	}()
	pid := 0
	if len(mine) > 0 && s.PlatformPID != nil {
		pid = s.PlatformPID()
	}
	type step struct {
		t    bluez.Transport
		next platformView
	}
	var todo []step
	s.mu.Lock()
	if pid != s.hidePID {
		s.view, s.hidePID = nil, pid
	}
	view := map[dbus.ObjectPath]platformView{} // пропавшие транспорты забываются: путь может вернуться с новым подключением
	for _, t := range mine {
		v := s.view[t.Path]
		view[t.Path] = v
		want := viewForgotten
		if t.Active() && s.volumeMode() == VolumeSpeaker {
			want = viewIdle
		}
		if v != want {
			todo = append(todo, step{t, want})
		}
	}
	s.view = view
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
	for _, st := range todo {
		var err error
		s.mu.Lock()
		prev := s.view[st.t.Path]
		s.mu.Unlock()
		if prev == viewOwn && st.t.Active() { // она могла уже счесть его телефоном: пусть отпустит фокус
			err = c.HideTransport(conns, st.t.Path)
		}
		switch {
		case err != nil:
		case st.next == viewForgotten:
			err = c.ForgetTransport(conns, st.t.Path)
		case prev == viewForgotten:
			err = c.ShowTransport(ctx, conns, st.t.Path)
		}
		if err != nil {
			s.note("скрыть канал в наушники от платформы: %v", err)
			continue
		}
		s.mu.Lock()
		s.view[st.t.Path] = st.next
		if st.next == viewForgotten {
			s.hides++
		}
		s.mu.Unlock()
		if st.next == viewForgotten {
			s.logf("канал в наушники скрыт от платформы: она не примет его за телефон и не остановит музыку")
		} else {
			s.logf("канал в наушники включён; платформе он показан простаивающим — она видит громкость наушников, но не их звук")
		}
	}
}
