# libs — общие библиотеки для компонентов

Код, который нужен нескольким компонентам (или не относится к ядру), но сам компонентом не является. В бинарник попадает только
то, что импортируют вошедшие в сборку компоненты.

| Папка | Что это | Кому нужно |
|---|---|---|
| [`gstreamer/`](gstreamer/README.md) | запуск встроенного GStreamer колонки (`maind --gst-launch`) | `yandex-music`, `system-sounds`, `aac` |
| [`aac/`](aac/README.md) | заголовок ADTS и декодер AAC → PCM на GStreamer колонки | `yandex-music`, `soundcloud`, `hls` |
| [`hls/`](hls/README.md) | приём звукового потока HLS: плейлисты, сегменты, звук из MPEG-TS, fMP4, голого AAC и MP3 | `yandex-music` (радио), `soundcloud` |
| [`dbus/`](dbus/README.md) | свой клиент системной шины D-Bus | `bluez` |
| [`bluez/`](bluez/README.md) | BlueZ и BlueALSA: устройства, спаривание, PCM | `bt-out`, `bt-in`, `bt-command` |
| [`bt-command/`](bt-command/README.md) | команда терминала `openmediad bt` | приходит вместе с `bt-out` или `bt-in` |
| [`alsactl/`](alsactl/README.md) | регулятор громкости ALSA (Master) и статус PCM | `bt-out`, инструменты разработчика |
| [`pcmstream/`](pcmstream/README.md) | звук от чужого процесса как источник движка (буфер, flush, возврат при паузе) | `spotify-soloist` |
| [`wsclient/`](wsclient/README.md) | клиент WebSocket | `spotify-soloist` |
| [`resample/`](resample/README.md) | передискретизация PCM (44,1 → 48 кГц) | `spotify-soloist` |
| [`ringled/`](ringled/README.md) | кольцо подсветки логотипа Мини 3 (`/dev/fb0`) и хранитель кадра | `led-ring` |
| [`clockled/`](clockled/README.md) | дисплей с часами Мини 3 (32 сегмента в sysfs), анимации прошивки `.binscled.zst` и проигрыватель с возвратом времени | `led-ring` |
| [`volmap/`](volmap/README.md) | таблица громкости платформы: уровень 0–20 ↔ Master | `bt-out`, `spotify-soloist` |
| [`debpkg/`](debpkg/README.md) | файлы из пакета .deb (ar + xz) | `spotify-soloist` |

Библиотеки могут импортировать только `core` и друг друга; компоненты — не наоборот.
