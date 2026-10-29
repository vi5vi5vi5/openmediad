# core/speaker/aplay — системный aplay

Звук на колонку идёт процессом `/usr/bin/aplay`, а не собственным вызовом ALSA: libasound и общий смеситель `dmix` на колонке
32-битные, 64-битный клиент мог бы испортить общую память смесителя.

**Главное правило: `aplay` никогда не получает SIGKILL** — только закрытие stdin (доиграет) или SIGINT.

| Файл | Что делает |
|---|---|
| `aplay.go` | `Config`, `Start`, `Player` (`Write`, `Finish` — доиграть, `Abort` — SIGINT), `ErrStuck` |
| `output.go` | `Output` — обёртка `Player`, реализующая `audio.Output` |
| `pipe_linux.go`, `pipe_other.go` | размер трубы к `aplay` (`F_SETPIPE_SZ`): меньше труба — меньше задержка |

**Кто использует:** `speaker`, `cli/test-sound`, компоненты `bt-out`, `bt-in`.
