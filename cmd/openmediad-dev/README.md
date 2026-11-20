# cmd/openmediad-dev — инструменты разработчика

Собираются `go run ./tools/build -dev`. Наблюдение за шиной, разбор записей трафика, замеры задержки, обезличивание записей.

| Файл | Что делает |
|---|---|
| `main.go` | таблица команд и диспетчер |
| `bus.go` | `observe`, `pcap`, `timeline`, `shimcheck`, `pcmwatch`, `sanitize` |
| `audio.go`, `voiceplay_*.go` | `play-yandex` (поток Яндекса или радио через движок без шины; `-silent` — без звука), `play-soundcloud` (SoundCloud по ссылке через движок без демона; `-proxy`, `-silent`), `voice-play` (задержка «чанк голоса → aplay») |
| `mixer.go` | `mixer` — регулятор ALSA без `amixer` |
| `exp_btidle.go` | `bt-idle` — одноразовый опыт с D-Bus (история — docs/research/HISTORY.md) |
