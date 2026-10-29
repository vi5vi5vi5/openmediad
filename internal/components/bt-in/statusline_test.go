package btin

import "testing"

func TestStatusLine(t *testing.T) {
	if got := statusLine(State{}); got != "Bluetooth недоступен (нет связи с BlueZ)" {
		t.Fatalf("нет шины: %q", got)
	}
	st := State{Bus: true, AplayOff: true}
	if got := statusLine(st); got != "не подключён; bluealsa-aplay остановлен" {
		t.Fatalf("%q", got)
	}
	st.Phone, st.Codec, st.Rate, st.Running, st.Streaming = "CC:FF", "SBC", 44100, true, true
	if got := statusLine(st); got != "CC:FF подключён (SBC, 44100 Гц), играет через openmediad; bluealsa-aplay остановлен" {
		t.Fatalf("%q", got)
	}
}
