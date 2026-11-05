package ringled

import (
	"bytes"
	"context"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MaindLog — лог основного процесса прошивки; в него device_state_handler пишет каждое переключение анимаций подсветки.
const MaindLog = "/data/quasar/daemons_logs/maind.log"

// Anim — что прошивка сейчас показывает на кольце. Собирается из строки лога вида
//
//	Play animations: [ .../alice_listening.led ] [ .../alice_listening.binscled.zst ] source=onSDKState
//
// где первые скобки — анимации кольца, вторые — часов.
type Anim struct {
	Ring  []string
	Clock []string  // анимации дисплея с часами: «<Animation>» (время), spectrogram, thinkingWithDelay, clock/alice_*.binscled.zst …
	At    time.Time // когда строка появилась в логе (нулевое — найдена при старте)
}

// RingIdle — кольцо в покое: прошивка закончила на idle.led (всё погашено) или ничего не показывает.
func (a Anim) RingIdle() bool {
	return len(a.Ring) == 0 || path.Base(a.Ring[len(a.Ring)-1]) == "idle.led"
}

// ClockIdle — дисплей свободен: прошивка показывает время («<Animation>») или ничего не сообщала. Спектр музыки, «думаю» и анимации Алисы — заняты.
func (a Anim) ClockIdle() bool {
	return len(a.Clock) == 0 || a.Clock[len(a.Clock)-1] == "<Animation>"
}

var playRe = regexp.MustCompile(`Play animations: \[ (.*?) \] \[ (.*?) \]`)

func parseLine(line []byte) (Anim, bool) {
	if !bytes.Contains(line, []byte("Play animations:")) {
		return Anim{}, false
	}
	m := playRe.FindSubmatch(line)
	if m == nil {
		return Anim{}, false
	}
	var ring []string
	for _, f := range strings.Fields(string(m[1])) {
		ring = append(ring, path.Base(f))
	}
	var clock []string
	for _, f := range strings.Fields(string(m[2])) {
		clock = append(clock, path.Base(f))
	}
	return Anim{Ring: ring, Clock: clock}, true
}

// Watcher следит за логом прошивки и помнит последнее переключение анимаций.
type Watcher struct {
	Path  string        // лог (по умолчанию MaindLog)
	Every time.Duration // как часто заглядывать в лог (по умолчанию 100 мс)

	mu   sync.Mutex
	cur  Anim
	off  int64 // сколько байт уже прочитано; -1 — ещё не читали
	part []byte
	init bool
}

// Current — последнее известное состояние анимаций.
func (w *Watcher) Current() Anim {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// Run опрашивает лог, пока не отменят ctx.
func (w *Watcher) Run(ctx context.Context) {
	if w.Path == "" {
		w.Path = MaindLog
	}
	if w.Every <= 0 {
		w.Every = 100 * time.Millisecond
	}
	t := time.NewTicker(w.Every)
	defer t.Stop()
	for {
		w.Poll(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// startTail — сколько хвоста лога читаем при старте, чтобы найти последнее переключение.
const startTail = 64 << 10

// Poll дочитывает новые строки. Файл открывается заново каждый раз: при ротации maind.log переименовывается в maind.log.1,
// и открытый дескриптор ушёл бы вместе с ним.
func (w *Watcher) Poll(now time.Time) {
	f, err := os.Open(w.Path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	first := !w.init
	if first {
		w.init = true
		w.off, w.part = max(0, st.Size()-startTail), nil
	} else if st.Size() < w.off {
		w.off, w.part = 0, nil // файл ротировали
	}
	if st.Size() == w.off {
		return
	}
	buf := make([]byte, st.Size()-w.off)
	n, _ := f.ReadAt(buf, w.off)
	w.off += int64(n)
	data := append(w.part, buf[:n]...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		w.part = data
		return
	}
	w.part = append([]byte(nil), data[end+1:]...)
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		a, ok := parseLine(line)
		if !ok {
			continue
		}
		if !first {
			a.At = now // строки, найденные при старте, уже «старые»: At остаётся нулевым
		}
		w.cur = a
	}
}
