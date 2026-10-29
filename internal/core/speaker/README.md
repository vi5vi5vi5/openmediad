# core/speaker — выход «динамики колонки»

Выход `speaker` есть всегда (не компонент): запускает системный `aplay` на устройстве ALSA `default` (общий смеситель колонки).

| Файл / папка | Что делает |
|---|---|
| `speaker.go` | `Sink`: имя `speaker`, `Available()` всегда `true`, `Open(format)` |
| [`aplay/`](aplay/README.md) | запуск и остановка процесса `aplay`, обёртка `audio.Output` |
