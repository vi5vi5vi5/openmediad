package btout

import (
	"fmt"
	"strings"
)

// State — снимок для status и отладочной страницы.
type State struct {
	Bus       bool   `json:"bus"`
	Connected bool   `json:"connected"`
	Device    string `json:"device,omitempty"`
	Name      string `json:"name,omitempty"`
	Rate      uint32 `json:"rate,omitempty"`
	Volume    int    `json:"volume_127"`
	Want      string `json:"want,omitempty"`
	Channel   bool   `json:"channel"`         // постоянный канал (смеситель) открыт
	Streams   int    `json:"streams"`         // сколько потоков движка звучит в наушниках
	Hidden    int    `json:"hidden_platform"` // сколько транспортов наушников скрыто от платформы
	Buttons   bool   `json:"buttons"`         // кнопки наушников читаются
}

// StatusLine — строка о наушниках для `openmediad status`.
func (s *Sink) StatusLine() string { return statusLine(s.DebugState().(State)) }

func statusLine(st State) string {
	if !st.Bus {
		return "Bluetooth недоступен (нет связи с BlueZ)"
	}
	if !st.Connected {
		if st.Want != "" {
			return "не подключены (выбраны " + st.Want + "); звук идёт по следующему выходу маршрута"
		}
		return "не подключены; звук идёт по следующему выходу маршрута"
	}
	line := fmt.Sprintf("%s (%s) подключены", st.Name, st.Device)
	if st.Volume >= 0 {
		line += fmt.Sprintf(", громкость %d/127 за колонкой", st.Volume)
	}
	if !st.Channel {
		return line + "; канал в наушники не открыт"
	}
	line += fmt.Sprintf("; канал открыт, потоков %d", st.Streams)
	if st.Buttons {
		line += "; кнопки наушников работают"
	}
	return line
}

// DebugState — что с наушниками сейчас.
func (s *Sink) DebugState() any {
	s.mu.Lock()
	st := State{Bus: s.busUp, Volume: s.lastVol, Want: strings.ToUpper(s.Device), Hidden: s.hides, Buttons: s.keys != nil}
	m := s.mix
	s.mu.Unlock()
	if m != nil && m.Err() == nil {
		st.Channel, st.Streams = true, m.Streams()
	}
	if t := s.cur.Load(); t != nil {
		st.Connected, st.Device, st.Name, st.Rate = true, t.addr, t.name, t.rate
	}
	return st
}
