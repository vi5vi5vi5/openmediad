# libs — общие библиотеки для компонентов

Код, который нужен нескольким компонентам (или не относится к ядру), но сам компонентом не является. В бинарник попадает только
то, что импортируют вошедшие в сборку компоненты.

| Папка | Что это | Кому нужно |
|---|---|---|
| [`gstreamer/`](gstreamer/README.md) | запуск встроенного GStreamer колонки (`maind --gst-launch`) | `yandex-music`, `system-sounds` |
| [`dbus/`](dbus/README.md) | свой клиент системной шины D-Bus | `bluez` |
| [`bluez/`](bluez/README.md) | BlueZ и BlueALSA: устройства, спаривание, PCM | `bt-out`, `bt-in`, `bt-command` |
| [`bt-command/`](bt-command/README.md) | команда терминала `openmediad bt` | приходит вместе с `bt-out` или `bt-in` |
| [`alsactl/`](alsactl/README.md) | регулятор громкости ALSA (Master) и статус PCM | `bt-out`, инструменты разработчика |
| [`aliced-client/`](aliced-client/README.md) | клиент aliced: пауза, «дальше», текстовый запрос Алисе | `bt-out` |

Библиотеки могут импортировать только `core` и друг друга; компоненты — не наоборот.
