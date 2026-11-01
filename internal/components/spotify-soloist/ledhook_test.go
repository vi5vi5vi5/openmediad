package spotifysoloist

import (
	"sync"
	"testing"

	"openmediad/internal/core/led"
)

type recLED struct {
	mu  sync.Mutex
	log []string
}

func (r *recLED) On(role led.Role, name string, def led.Color) {
	r.mu.Lock()
	r.log = append(r.log, "on "+name+" "+def.Hex())
	r.mu.Unlock()
}

func (r *recLED) Off(role led.Role, name string) {
	r.mu.Lock()
	r.log = append(r.log, "off "+name)
	r.mu.Unlock()
}

func (r *recLED) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.log) == 0 {
		return ""
	}
	return r.log[len(r.log)-1]
}

func TestSpotifyPlaybackLightsTheSourceIndicator(t *testing.T) {
	s, _ := newTestServer(t)
	rec := &recLED{}
	c := &Component{led: rec, pcm: s}
	s.onEvent = c.onPlayChange
	conn := connect(t, s)
	send(t, conn, hello(48000, 2), cork(false), frame(frData, pcm(240)))
	waitFor(t, "Spotify зажёг индикатор источника", func() bool { return rec.last() == "on spotify-soloist #00ff40" })
	send(t, conn, cork(true))
	waitFor(t, "пауза погасила индикатор", func() bool { return rec.last() == "off spotify-soloist" })
}

func TestPlayChangeWithoutLEDOrServerIsHarmless(t *testing.T) {
	(&Component{}).onPlayChange()
	(&Component{led: &recLED{}}).onPlayChange()
}
