package btout

import (
	"testing"

	"openmediad/internal/core/led"
)

// Наушники сами предлагают синий цвет подсветки: демон берёт его через интерфейс LEDColor.
func TestSinkSuggestsBlueLED(t *testing.T) {
	var s interface{ LEDColor() led.Color } = &Sink{}
	if got := s.LEDColor(); got != (led.Color{R: 0, G: 96, B: 255}) {
		t.Fatalf("цвет наушников %v", got)
	}
}
