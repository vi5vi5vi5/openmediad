package ctlsock

// Status — ответ демона на команду status (его печатает `openmediad status`). Лежит рядом с сокетом, а не в пакете демона,
// чтобы терминальные команды не зависели от всего демона.
type Status struct {
	Version     string            `json:"version"`
	PID         int               `json:"pid"`
	Mode        string            `json:"mode"`
	UptimeS     int64             `json:"uptime_s"`
	Config      string            `json:"config"`
	Warnings    []string          `json:"warnings,omitempty"`
	Components  []ComponentStatus `json:"components"`
	Music       bool              `json:"music"` // что openmediad играет сам
	Voice       bool              `json:"voice"`
	Sounds      bool              `json:"sounds"`
	Connections int               `json:"connections"` // соединений платформы через шим (норма — 4)
	LogLevel    string            `json:"log_level"`
	HeapKB      uint64            `json:"heap_kb"`
	Goroutines  int               `json:"goroutines"`
	Stock       *StockUse         `json:"stock,omitempty"`   // что ещё делает штатный mediad
	Players     any               `json:"players,omitempty"` // с Detail: плееры автомата mediad
	Details     map[string]any    `json:"details,omitempty"` // с Detail: DebugState каждого компонента по его имени
}

// StockUse — что с запуска демона досталось штатному mediad. Пока в Played что-то есть, без штатного плеера этот звук пропадёт.
type StockUse struct {
	SinceS   int64        `json:"since_s"`  // сколько секунд идёт учёт
	Played   []StockCount `json:"played"`   // штатный mediad сам начал играть (по виду плеера)
	Returned []StockCount `json:"returned"` // команды, которые движок взял, но вернул: не смог до первого звука
	Passed   []StockCount `json:"passed"`   // команды платформы, которые движок пропустил как есть
}

// StockCount — счётчик: что, сколько раз и как давно в последний раз.
type StockCount struct {
	What string `json:"what"`
	N    uint64 `json:"n"`
	AgoS int64  `json:"ago_s"`
}

// ComponentStatus — компонент сборки в демоне.
type ComponentStatus struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	Line    string `json:"line,omitempty"` // строка для `openmediad status` от самого компонента (StatusLine)
}
