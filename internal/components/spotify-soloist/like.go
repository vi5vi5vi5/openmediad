package spotifysoloist

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/engine"
	"openmediad/internal/libs/pcmstream"
)

// Лайки. Фраза («Алиса, лайк», «мне нравится», «убери лайк») и директива облака player_like ставят или снимают лайк играющему треку
// Spotify через Web API (webapi.go). Вызов сети идёт в фоне, фраза считается выполненной сразу; результат подтверждает короткий сигнал:
// два восходящих тона — готово, один низкий — не вышло (причина в журнале). Дизлайка у Spotify нет, поэтому такие фразы идут Алисе.

// likeHintEvery — как часто напоминать в журнале, что для лайков нужен вход (фразы «лайк» слышны часто).
const likeHintEvery = 10 * time.Minute

// startLike запускает постановку (liked) или снятие лайка у играющего трека. false — лайк не начат (нет входа, играет не трек):
// фраза остаётся Алисе.
func (c *Component) startLike(ps playerState, liked bool) bool {
	if !c.config().Like || c.web == nil {
		return false
	}
	if err := c.web.ready(); err != nil {
		c.mu.Lock()
		hint := time.Since(c.likeHintAt) > likeHintEvery
		if hint {
			c.likeHintAt = time.Now()
		}
		c.mu.Unlock()
		if hint {
			c.logf("лайк не поставлен: %v", err)
		}
		return false
	}
	if _, ok := trackID(ps.URI); !ok {
		c.logf("лайк: играет не трек Spotify (%q)", ps.URI)
		return false
	}
	go c.doLike(ps.URI, ps.Title, liked)
	return true
}

func (c *Component) doLike(uri, title string, liked bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	what := "лайк поставлен"
	if !liked {
		what = "лайк снят"
	}
	if err := c.web.setSaved(ctx, uri, liked); err != nil {
		c.mu.Lock()
		c.likeErr = err.Error()
		c.mu.Unlock()
		c.logf("лайк не удался: %v", err)
		c.beep(false)
		return
	}
	c.mu.Lock()
	c.likes++
	c.likeErr = ""
	c.mu.Unlock()
	c.logf("%s: %s", what, title)
	c.beep(true)
}

var beepSeq atomic.Uint64

// beep играет короткий сигнал через движок (класс голоса: идёт туда же, куда голос Алисы, в том числе в наушники).
func (c *Component) beep(ok bool) {
	if c.eng == nil {
		return
	}
	f := audio.Format{Rate: engineRate, Channels: 2}
	src := pcmstream.New(f, 2*time.Second, 0)
	src.Write(confirmTone(ok, engineRate))
	src.CloseWrite()
	var st *engine.Stream
	st = c.eng.NewStream(fmt.Sprintf("spotify-beep/%d", beepSeq.Add(1)), engine.Voice, src, engine.Options{Spectrum: engine.NoSpectrum, Endless: true},
		engine.Events{
			Ended:  func() { go st.Close() },
			Failed: func(err error) { c.logf("сигнал не сыграл: %v", err); go st.Close() },
		})
	st.Play()
}

// confirmTone — сигнал подтверждения, стерео S16LE: «готово» (две восходящие ноты) или «не вышло» (одна низкая).
func confirmTone(ok bool, rate int) []byte {
	type note struct {
		hz  float64
		dur time.Duration
	}
	notes := []note{{220, 260 * time.Millisecond}}
	if ok {
		notes = []note{{1175, 70 * time.Millisecond}, {1568, 130 * time.Millisecond}}
	}
	const amp = 0.28 * math.MaxInt16
	var out []byte
	for _, n := range notes {
		frames := int(float64(rate) * n.dur.Seconds())
		for i := 0; i < frames; i++ {
			t := float64(i) / float64(rate)
			env := math.Sin(math.Pi * float64(i) / float64(frames)) // плавное нарастание и затухание: без щелчков
			v := int16(amp * env * math.Sin(2*math.Pi*n.hz*t))
			out = append(out, byte(v), byte(v>>8), byte(v), byte(v>>8))
		}
	}
	return out
}
