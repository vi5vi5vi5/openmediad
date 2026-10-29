# alice-voice/opus — декодер Ogg/Opus

Декодер потока Ogg/Opus (RFC 7845) в PCM S16LE моно поверх `github.com/tphakala/go-opus` (чистый Go, без CGO). Один долгоживущий декодер,
между ответами — `Reset`. `PreSkipTap` отдаёт спектру выброшенные отсчёты pre-skip (так считает штатный `mediad`).
