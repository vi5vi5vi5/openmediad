package btout

import (
	"strings"
	"testing"
)

func TestStatusLine(t *testing.T) {
	if got := statusLine(State{}); got != "Bluetooth недоступен (нет связи с BlueZ)" {
		t.Fatalf("нет шины: %q", got)
	}
	if got := statusLine(State{Bus: true, Want: "AA:BB"}); got != "не подключены (выбраны AA:BB); звук идёт по следующему выходу маршрута" {
		t.Fatalf("не подключены: %q", got)
	}
	st := State{Bus: true, Connected: true, Name: "ROG", Device: "A0:AD", Volume: 63}
	if got := statusLine(st); got != "ROG (A0:AD) подключены, громкость 63/127 за колонкой; канал в наушники не открыт" {
		t.Fatalf("без канала: %q", got)
	}
	st.Channel, st.Streams = true, 2
	if got := statusLine(st); !strings.HasSuffix(got, "; канал открыт, потоков 2") {
		t.Fatalf("с каналом: %q", got)
	}
	st.Buttons = true
	if got := statusLine(st); !strings.HasSuffix(got, "; кнопки наушников работают") {
		t.Fatalf("с кнопками: %q", got)
	}
}

// Директивы Алисы «выключи / включи блютуз» понимает только этот компонент: ядро отдаёт ему имена директив как есть.
func TestOnDirectiveSwitchesBluetooth(t *testing.T) {
	s := &Sink{btCmd: make(chan bool, 1)}
	s.OnDirective("stop_bluetooth.something")
	if on := <-s.btCmd; on {
		t.Fatal("stop_bluetooth должен выключать")
	}
	s.OnDirective("start_bluetooth")
	if on := <-s.btCmd; !on {
		t.Fatal("start_bluetooth должен включать")
	}
	s.OnDirective("player_pause") // чужая директива — ничего не происходит
	select {
	case <-s.btCmd:
		t.Fatal("чужая директива не должна ничего менять")
	default:
	}
}
