package spotifysoloist

import "openmediad/internal/core/led"

// ledColor — цвет, который Spotify предлагает для подсветки (зелёный).
var ledColor = led.Color{R: 0, G: 255, B: 64}

// onPlayChange вызывается, когда поток Spotify начал или перестал звучать: подсветка показывает источник, пока Spotify играет.
func (c *Component) onPlayChange() {
	if c.led == nil || c.pcm == nil {
		return
	}
	if c.pcm.Playing() {
		c.led.On(led.Source, Name, ledColor)
	} else {
		c.led.Off(led.Source, Name)
	}
}
