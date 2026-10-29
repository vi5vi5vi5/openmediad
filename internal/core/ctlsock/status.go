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
	Players     any               `json:"players,omitempty"` // с Detail: плееры автомата mediad
	Details     map[string]any    `json:"details,omitempty"` // с Detail: DebugState каждого компонента по его имени
}

// ComponentStatus — компонент сборки в демоне.
type ComponentStatus struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	Line    string `json:"line,omitempty"` // строка для `openmediad status` от самого компонента (StatusLine)
}
