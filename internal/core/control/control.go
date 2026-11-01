// Package control — единая дверь для команд плееру: пауза, продолжить, дальше, назад — откуда бы они ни пришли.
//
// Команду может подать кто угодно: кнопки и жесты на колонке, кнопки наушников, голос. Каждому такому источнику незачем знать, какие плееры
// есть в сборке и чей сейчас звук. Он говорит Hub «пришло такое-то, вот откуда», а Hub по очереди спрашивает плееры, и первый, кому команда
// по праву принадлежит, выполняет её. Не взял никто (играет Яндекс.Музыка или не играет ничего) — Do возвращает false, и источник поступает
// как раньше, обычно отдаёт команду платформе.
//
// Плеер — компонент, который играет музыку сам и про которого платформа не знает (сейчас Spotify). Источник и плеер друг о друге не знают.
package control

import "sync"

// Cmd — команда плееру.
type Cmd string

const (
	Pause  Cmd = "pause"  // поставить на паузу
	Resume Cmd = "resume" // продолжить
	Toggle Cmd = "toggle" // пауза, если играет, и продолжить, если стоит: так просят кнопки
	Next   Cmd = "next"   // следующий трек
	Prev   Cmd = "prev"   // предыдущий трек
)

func (c Cmd) String() string {
	switch c {
	case Pause:
		return "пауза"
	case Resume:
		return "продолжить"
	case Toggle:
		return "пауза/продолжить"
	case Next:
		return "следующий трек"
	case Prev:
		return "предыдущий трек"
	}
	return string(c)
}

// Origin — откуда пришла команда.
type Origin string

const (
	Speaker    Origin = "speaker"    // кнопка Алисы и жесты по верху колонки
	Headphones Origin = "headphones" // кнопки на Bluetooth-наушниках
)

func (o Origin) String() string {
	switch o {
	case Speaker:
		return "кнопка или жест на колонке"
	case Headphones:
		return "кнопка наушников"
	}
	return string(o)
}

// PlatformReacts говорит, видит ли то же нажатие платформа и ответит ли на него по-своему: поставит на паузу свой плеер, а на жест вперёд-назад
// запросит у облака очередь Яндекс.Музыки. Наушники платформа не слышит: их кнопки читает только движок.
func (o Origin) PlatformReacts() bool { return o == Speaker }

// Player — плеер, который умеет выполнять команды.
type Player interface {
	// Handle выполняет команду, если плеер сейчас хозяин звука. false — команда не его (играет другой источник, нет входа, выключено в настройках).
	Handle(c Cmd, from Origin) bool
}

type member struct {
	name string
	p    Player
}

// Hub собирает плееры и раздаёт им команды.
type Hub struct {
	// OnHandled (необязательный) вызывается, когда плеер выполнил команду: для журнала.
	OnHandled func(c Cmd, from Origin, player string)

	mu      sync.Mutex
	players []member
}

// Add добавляет плеер. Спрашивают плееры в том порядке, в каком их добавили.
func (h *Hub) Add(name string, p Player) {
	h.mu.Lock()
	h.players = append(h.players, member{name, p})
	h.mu.Unlock()
}

// Len — сколько плееров подключено.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.players)
}

// Do отдаёт команду плеерам по очереди; тот, кто её выполнил, останавливает обход: одну команду не должны выполнить два плеера. false — никто не взял.
func (h *Hub) Do(c Cmd, from Origin) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	list := append([]member(nil), h.players...)
	h.mu.Unlock()
	for _, m := range list { // без замка: плеер может думать долго
		if m.p.Handle(c, from) {
			if h.OnHandled != nil {
				h.OnHandled(c, from, m.name)
			}
			return true
		}
	}
	return false
}
