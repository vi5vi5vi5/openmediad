// Package buttons — кнопка «Алиса» и жесты по верху колонки, как их видит платформа.
//
// Нажатие и проведение по колонке платформа превращает в команды плееру: «Toggle play/pause», «Request next song», «Request previous song»
// (buttons_actions.cpp в maind.log). Команды она адресует своей аудиосессии, а Spotify такой сессии у платформы нет: пауза уходит в пустоту,
// а проведение вперёд-назад платформа превращает в запрос облаку, и оно запускает очередь Яндекс.Музыки. На шине mediad от кнопки не остаётся
// ничего, зато в логе платформы есть строка, и она появляется за несколько миллисекунд до запроса облаку. По ней компонент успевает выполнить
// действие в Spotify и подготовить автомат к запуску Яндекса (ArmCarrier).
package buttons

import (
	"bytes"
	"context"
	"os"
	"sync"
	"time"
)

// PlatformLog — лог основного процесса прошивки.
const PlatformLog = "/data/quasar/daemons_logs/maind.log"

// Action — что колонка записала в лог по нажатию или жесту.
type Action string

const (
	Toggle Action = "toggle" // нажатие кнопки Алисы: пауза или продолжить
	Next   Action = "next"   // проведение вперёд: следующий трек
	Prev   Action = "prev"   // проведение назад: предыдущий трек
)

func (a Action) String() string {
	switch a {
	case Toggle:
		return "пауза/продолжить (кнопка)"
	case Next:
		return "следующий трек (жест)"
	case Prev:
		return "предыдущий трек (жест)"
	}
	return string(a)
}

// Parse узнаёт действие в строке лога платформы.
func Parse(line []byte) (Action, bool) {
	if !bytes.Contains(line, []byte("buttons_actions.cpp")) {
		return "", false
	}
	switch {
	case bytes.Contains(line, []byte("Toggle play/pause")):
		return Toggle, true
	case bytes.Contains(line, []byte("Request next song")):
		return Next, true
	case bytes.Contains(line, []byte("Request previous song")):
		return Prev, true
	}
	return "", false
}

// Watcher читает лог платформы с конца и вызывает OnAction для каждого нового действия кнопок.
type Watcher struct {
	Path     string        // лог (по умолчанию PlatformLog)
	Every    time.Duration // как часто заглядывать в лог (по умолчанию 50 мс: облако отвечает на жест не раньше чем через 0,3 с)
	OnAction func(Action)

	mu   sync.Mutex
	f    os.FileInfo // файл, который читаем: смена файла — ротация лога
	off  int64
	part []byte
}

// Run опрашивает лог, пока не отменят ctx.
func (w *Watcher) Run(ctx context.Context) {
	if w.Path == "" {
		w.Path = PlatformLog
	}
	every := w.Every
	if every <= 0 {
		every = 50 * time.Millisecond
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w.Poll()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll дочитывает новые строки. Первый раз читает с конца: прошлые нажатия не повторяются. Файл открывается заново каждый раз: при ротации
// maind.log переименовывается в maind.log.1 и открытый дескриптор ушёл бы вместе с ним.
func (w *Watcher) Poll() {
	f, err := os.Open(w.Path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	var actions []Action
	w.mu.Lock()
	switch {
	case w.f == nil:
		w.off, w.part = st.Size(), nil
	case !os.SameFile(w.f, st) || st.Size() < w.off:
		w.off, w.part = 0, nil // файл ротировали: новый читаем с начала
	}
	w.f = st
	if st.Size() > w.off {
		buf := make([]byte, st.Size()-w.off)
		n, _ := f.ReadAt(buf, w.off)
		w.off += int64(n)
		data := append(w.part, buf[:n]...)
		if end := bytes.LastIndexByte(data, '\n'); end < 0 {
			w.part = data
		} else {
			w.part = append([]byte(nil), data[end+1:]...)
			for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
				if a, ok := Parse(line); ok {
					actions = append(actions, a)
				}
			}
		}
	}
	w.mu.Unlock()
	if w.OnAction != nil {
		for _, a := range actions { // без замка: обработчик может долго работать
			w.OnAction(a)
		}
	}
}
