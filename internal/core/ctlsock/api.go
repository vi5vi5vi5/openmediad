package ctlsock

// Управление через сокет команд: то, чем пользуются `openmediad player`, `events`, `ask` и свои программы (docs/API.md).
// Запрос — строка JSON, ответ — строки {"ok":true,"data":…}; типы ниже — содержимое data.

// Действия команды control (Request.Action).
const (
	ActionPause  = "pause"
	ActionResume = "resume"
	ActionToggle = "toggle"
	ActionNext   = "next"
	ActionPrev   = "prev"
)

// ControlResult — ответ на control: кто выполнил команду.
type ControlResult struct {
	Action string `json:"action"`
	// By — имя плеера движка (как компонент: spotify-soloist) или platform-music — команда передана музыке платформы, как нажатие кнопки на колонке.
	By string `json:"by"`
}

// PlayerState — плеер движка глазами ядра (control.State).
type PlayerState struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`              // готов выполнять команды
	Playing   bool   `json:"playing"`            // играет или вот-вот заиграет
	Sounding  bool   `json:"sounding"`           // звук течёт в движок
	Paused    bool   `json:"paused"`             // на паузе
	Resumable bool   `json:"resumable"`          // на паузе, и «продолжи» достанется ему
	Track     string `json:"track,omitempty"`    // что-то, что меняется вместе с треком
	NearEnd   bool   `json:"near_end,omitempty"` // трек доигрывается
}

// PlatformTrack — плеер, который ведёт автомат mediad (музыка платформы, голос, звонки).
type PlatformTrack struct {
	State string `json:"state"`
	Title string `json:"title,omitempty"`
	PosMs uint64 `json:"pos_ms"`
	DurMs uint64 `json:"dur_ms,omitempty"`
}

// Playing — ответ на players: кто играет сейчас.
type Playing struct {
	// Owner — кто последним начал играть: имя плеера движка, platform-music или пусто (ещё никто). Ему достанется «продолжи».
	Owner string `json:"owner"`
	// Dialog — идёт диалог с Алисой (она слушает, думает или говорит): музыка на это время приглушена.
	Dialog bool `json:"dialog"`
	// PlatformPlaying — играет музыка платформы (Яндекс.Музыка), которую ведёт движок.
	PlatformPlaying bool            `json:"platform_playing"`
	Players         []PlayerState   `json:"players"`  // плееры движка
	Platform        []PlatformTrack `json:"platform"` // плееры платформы, которые ведёт движок
}

// Виды событий (Event.Type).
const (
	EventPlayer = "player" // пауза, продолжение, перемотка: команда выполнена или плеер изменился сам
	EventDialog = "dialog" // диалог с Алисой начался или кончился
)

// Event — событие для подписчика (команда events): одна строка JSON на событие.
type Event struct {
	Type string `json:"type"`  // player | dialog
	AtMs int64  `json:"at_ms"` // время, Unix-миллисекунды

	// для player
	Action string `json:"action,omitempty"` // pause | resume | next | prev
	From   string `json:"from,omitempty"`   // speaker | headphones | voice | fast | cloud | virtual | arbiter | app | api
	Player string `json:"player,omitempty"` // имя плеера движка или platform-music
	// PlatformDisplay и PlatformRing — прошивка сама покажет это действие на дисплее и на кольце логотипа.
	PlatformDisplay bool `json:"platform_display,omitempty"`
	PlatformRing    bool `json:"platform_ring,omitempty"`

	// для dialog
	On bool `json:"on,omitempty"`
}
