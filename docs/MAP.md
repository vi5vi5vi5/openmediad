# Карта проекта

Где что лежит и почему. Подробности по файлам — в `README.md` каждой папки; паспорта компонентов — [COMPONENTS.md](COMPONENTS.md).
Как работает демон — [ARCHITECTURE.md](ARCHITECTURE.md), протокол шины — [BUS-PROTOCOL.md](BUS-PROTOCOL.md).

## Оглавление

1. [Как это устроено за минуту](#1-как-это-устроено-за-минуту)
2. [Дерево проекта](#2-дерево-проекта)
3. [Три зоны и правила между ними](#3-три-зоны-и-правила-между-ними)
4. [Компоненты: имена, классы, теги, сборка](#4-компоненты-имена-классы-теги-сборка)
5. [Что такое GStreamer и зачем он здесь](#5-что-такое-gstreamer-и-зачем-он-здесь)
6. [Сквозные сценарии](#6-сквозные-сценарии)
7. [Где менять](#7-где-менять)
8. [Было → стало](#8-было--стало)
9. [Размер бинарника](#9-размер-бинарника)

---

## 1. Как это устроено за минуту

Бинарник один — `openmediad`, запускается двумя способами:

1. **Команда терминала** (`openmediad status`, `config`, `install` …) — процесс живёт долю секунды. Код: `internal/core/cli`.
2. **Демон** (`openmediad run`; его запускает страж `guard.sh`) — живёт всё время. Код: `internal/core/daemon`.

Демон стоит между платформой Алисы и штатным плеером `mediad`: правило iptables заворачивает TCP-соединения платформы с порта 9906 на порт
шима 19906. Каждый кадр шины демон либо **перехватывает** (музыка Яндекса, голос Алисы, звонки — их играет сам движок и сам же выпускает
события «как mediad»), либо **пропускает** штатному `mediad` байт в байт. Поэтому любая часть движка может отказаться от работы, и колонка
продолжит играть штатно.

---

## 2. Дерево проекта

```
openmediad/
├── README.md                    что это, как собрать, куда смотреть дальше
├── PLAN.md                      план и журнал решений (история проекта)
├── VERSION  go.mod  go.sum  THIRD_PARTY.md  vendor/  vendor.sha256
│
├── cmd/
│   ├── openmediad/              ПРОДУКТ: main.go (одна строка) + подключение каталога и набора компонентов
│   └── openmediad-dev/          инструменты разработчика (не часть продукта)
│
├── internal/
│   ├── core/                    ══ ЯДРО: без этого движок не существует; в бинарнике всегда ══
│   │   ├── daemon/              запуск и жизнь демона; единственное место, где всё соединяется
│   │   ├── engine/              потоки звука, классы (музыка / голос / звонок), громкость, позиция по часам
│   │   ├── audio/               формат PCM, кольцевой буфер, громкость, нарастание; договор Output
│   │   ├── spectrum/            математика спектра (БПФ): её вызывает движок
│   │   ├── speaker/             выход «динамики колонки» — есть всегда
│   │   │   └── aplay/           запуск системного aplay
│   │   ├── quasar/              язык шины Quasar и эмуляция штатного плеера
│   │   │   ├── pb/ wire/ msg/   protobuf без схем → кадры и конверт → типизированные «окна»
│   │   │   ├── shim/            посредник платформа (19906) ↔ настоящий mediad (9906)
│   │   │   ├── mediad/          эмуляция mediad: автоматы плееров «музыка / голос / звонки»
│   │   │   └── explain/ observe/ пояснения для журнала; слежение за диалогом Алисы
│   │   ├── registry/            механизм компонентов: паспорт Info, Register, Env
│   │   ├── config/              файл настроек: разбор, проверка, шаблон, правка ключей
│   │   ├── journal/             кольцевой журнал и файл с ротацией
│   │   ├── ctlsock/             сокет «терминал ↔ демон» + тип ответа status
│   │   ├── cli/                 команды терминала (status, config, install …)
│   │   │   └── command/         как добавить команду: сюда подключаются и компоненты
│   │   ├── platform/            «колонка как система»: layout, device, install, initd (страж), stock, iptables, proc, disk, clock
│   │   └── buildinfo/           версия, дата, коммит, теги сборки
│   │
│   ├── components/              ══ КОМПОНЕНТЫ: опциональные возможности, у каждого паспорт ══
│   │   ├── catalog/             ЕДИНАЯ ТАБЛИЦА паспортов (не компонент)
│   │   ├── included/            include_<имя>.go: что войдёт в бинарник по тегам сборки (не компонент)
│   │   ├── yandex-music/        Музыка Яндекса           (+ mp4/, aac/)
│   │   ├── alice-voice/         Голос Алисы              (+ ogg/, opus/)
│   │   ├── system-sounds/       Звонки и системные звуки (+ mp3/)
│   │   ├── display-spectrum/    Спектр для дисплея и подсветки
│   │   ├── debug-page/          Отладочная веб-страница
│   │   ├── debug-profiler/      Профилировщик pprof (требует debug-page)
│   │   ├── bt-out/              Bluetooth-наушники: вывод звука
│   │   └── bt-in/               Bluetooth: звук с телефона через движок
│   │
│   ├── libs/                    ══ ОБЩЕЕ ДЛЯ КОМПОНЕНТОВ: не ядро, но нужно нескольким компонентам ══
│   │   ├── gstreamer/           запуск встроенного GStreamer колонки         → yandex-music, system-sounds
│   │   ├── dbus/                свой клиент системной шины D-Bus             → bluez
│   │   ├── bluez/               BlueZ и BlueALSA: устройства, спаривание     → bt-out, bt-in, bt-command
│   │   ├── bt-command/          команда терминала `openmediad bt`            → приходит с bt-out или bt-in
│   │   ├── alsactl/             регулятор громкости ALSA и статус PCM        → bt-out, devtools
│   │   └── aliced-client/       клиент aliced: пауза / «дальше» / запрос Алисе → bt-out
│   │
│   ├── devtools/                ══ ИНСТРУМЕНТЫ РАЗРАБОТЧИКА: код для openmediad-dev и тестов ══
│   │   └── capture/             чтение записей трафика (pcap, .omdf)
│   └── architecture/            тест, который держит правила зависимостей
│
├── tools/
│   ├── build/                   сборка: профили, -with/-without, -list, -check, -deploy, -sync-docs
│   └── celtcheck/               отдельный модуль: сверка декодера Opus
├── scripts/dev/                 скрипты для живой колонки и симулятор колонки
├── testdata/                    обезличенные записи трафика для золотых тестов
└── docs/                        MAP.md (эта карта), COMPONENTS.md, ARCHITECTURE.md, INSTALL.md, BUS-PROTOCOL.md, research/
```

---

## 3. Три зоны и правила между ними

| Зона | Что это | В бинарнике |
|---|---|---|
| **`core/`** | без чего движок не существует | всегда |
| **`components/`** | опциональные возможности, у каждой паспорт | только выбранные при сборке |
| **`libs/`** | общие куски, нужные только компонентам | только если нужны вошедшим компонентам |

Правила импорта (проверяет `internal/architecture/architecture_test.go` при каждом `go test ./...`):

- `core` импортирует только `core`, и **в коде ядра нет ни одного имени компонента**;
- `libs` импортируют `core` и `libs`;
- компонент — `core`, `libs`, свои подпакеты и компоненты, указанные в его паспорте как зависимость;
- в каждой папке с кодом есть `README.md`.

Ядро и компоненты общаются через **точки расширения**, не зная друг друга по имени:

| Точка | Как работает |
|---|---|
| команда терминала | компонент или библиотека зовёт `command.Register(группа, команда)` из `init()` — так появляется `openmediad bt` |
| строка в `status` | компонент реализует `StatusLine() string` |
| директивы Алисы | компонент реализует `OnDirective(name string)`: демон отдаёт ему имена всех директив, смысл знает компонент |
| пробный выход | `command.RegisterTestOutput(имя, ...)` — `openmediad test-sound ИМЯ` |
| состояние для отладки | `DebugState() any` — показывается в `status -v` и на отладочной странице по имени компонента |

---

## 4. Компоненты: имена, классы, теги, сборка

Одно имя везде: **папка = запись в каталоге = секция файла настроек = имя в `-with` / `-without`**; тег сборки — `omd_` и то же имя с `_`
вместо `-` (Go не допускает дефис в теге).

| Компонент | Название | Класс | По умолчанию | Нужно от прошивки | Тег |
|---|---|---|---|---|---|
| `yandex-music` | Музыка Яндекса | необходимый | да | GStreamer, `aplay`, сертификаты, сеть | `omd_yandex_music` |
| `alice-voice` | Голос Алисы | необходимый | да | `aplay` | `omd_alice_voice` |
| `system-sounds` | Звонки и системные звуки | штатный | да | GStreamer, `aplay`, файлы звуков | `omd_system_sounds` |
| `display-spectrum` | Спектр для дисплея | штатный | да | сервис `iohub` | `omd_display_spectrum` |
| `debug-page` | Отладочная страница | вспомогательный | да | — | `omd_debug_page` |
| `debug-profiler` | Профилировщик | вспомогательный | нет | компонент `debug-page` | `omd_debug_profiler` |
| `bt-out` | Bluetooth-наушники (вывод) | необязательный, опытный | нет | BlueZ, `aplay`, `/dev/input`, `aliced` | `omd_bt_out` |
| `bt-in` | Bluetooth: звук с телефона | необязательный, опытный | нет | BlueZ, `arecord`, `bluealsa-aplay` | `omd_bt_in` |

Классы: **необходимый** — то, ради чего проект существует; **штатный** — повторяет штатное поведение колонки; **вспомогательный** — для людей,
не для звука; **необязательный** — новая возможность сверх штатной колонки. Любой компонент можно не включать в сборку — что он делал, играет
штатный `mediad` (в паспорте — строка «Если убрать»).

Сборка (`go run ./tools/build`): профили `core` (только ядро), `standard` (по умолчанию), `full` (всё готовое); `-with ИМЯ`, `-without ИМЯ`;
если выбранному компоненту нужен другой (`debug-profiler` → `debug-page`), инструмент добавит его сам. Голый `go build` без тегов даёт
ядро без компонентов — прозрачного посредника.

**Паспорт** — запись в `internal/components/catalog/catalog.go`. Правите её → `go run ./tools/build -sync-docs` переписывает
`docs/COMPONENTS.md` и блок «Паспорт» в `README.md` компонента (тест `TestDocsInSync` не даст забыть).

**Команда `bt`** приходит из `libs/bt-command` вместе с `bt-out` или `bt-in`. Без них нет ни команды, ни пакетов D-Bus и BlueZ в бинарнике.

---

## 5. Что такое GStreamer и зачем он здесь

**Что это.** Готовый мультимедийный каркас: из «кубиков» (источник, разборщик формата, декодер, преобразователь, вывод) собирается
конвейер вида `источник ! разборщик ! декодер ! вывод`. В прошивке колонки он уже есть: штатный `mediad` играет всё через него. Вшит в
`/system/vendor/quasar/maind` и запускается отдельной программой: `maind --gst-launch "<конвейер>"`.

**Что делает у нас — одно: превращает сжатый звук в «сырой» PCM.** Мы запускаем его *дочерним процессом*, подаём сжатые данные (в stdin
или путём к файлу) и читаем PCM из **третьего дескриптора** (`fd 3`): stdout и stderr у `maind` заняты его журналом. Дальше PCM идёт в наш
движок (громкость, спектр) и в `aplay`. В бинарник `openmediad` GStreamer не линкуется (сборка без CGO).

**Где участвует — ровно в двух местах:**

| Где | Что подаётся | Конвейер |
|---|---|---|
| музыка Яндекса (`yandex-music/aac`) | Go сам скачивает поток, расшифровывает, разбирает MP4, режет на кадры AAC и оборачивает в ADTS | `fdsrc fd=0 ! aacparse ! avdec_aac ! audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=2,format=S16LE ! fdsink fd=3 sync=false` |
| системные звуки (`system-sounds/library.go`) | файлы `/system/vendor/quasar/sounds/` (`timer.wav` — на самом деле MP3, `spotter_*.mp3`, jingle `.opus`) раскодируются целиком в память один раз | `filesrc location=<файл> ! decodebin ! audioconvert ! audioresample ! … ! fdsink fd=3 sync=false` |

**Где его нет.** Голос Алисы (Ogg/Opus) декодирует чистый Go (`go-opus`): GStreamer на одном ответе давал ≈30 МБ в пике и ≈0,5 с,
Go-декодер — ≈3 МБ и ≈25 мс на секунду звука (HISTORY.md, §13). Скачивание, расшифровка, разбор MP4 и Ogg, перемотка, громкость, спектр и
вывод звука (`aplay`) тоже на Go.

**Почему так.** (1) Декодера AAC на Go в проекте нет, а писать его — крупный подпроект (PLAN.md, §13.5, запас З5; MP3 для звонков — З4).
(2) GStreamer на колонке уже есть и проверен (решение Р11): никакого постороннего кода. (3) Отдельный процесс: ошибка в конвейере не роняет демон.

**Цена.** Процесс на каждый трек — ≈12 МБ Pss и ≈0,5 с на запуск; при раскодировании звуков из файлов — пик ≈30 МБ. На неверном конвейере
процесс **не завершается сам**, поэтому он всегда запускается со сторожем по времени; в отличие от `aplay`, ему можно посылать SIGKILL.

**В структуре:** обвязка — `internal/libs/gstreamer`; нужна двум компонентам, и это записано в их паспортах («Нужно от прошивки: GStreamer»).
`openmediad doctor` проверяет, что `maind` на месте, если один из них вошёл в сборку.

---

## 6. Сквозные сценарии

| Сценарий | Путь |
|---|---|
| **«Алиса, включи музыку»** | платформа → iptables → `core/quasar/shim` → `daemon/intercept.go` → `quasar/mediad` (`machine.go` → `audio.go` `Play`) → `components/yandex-music/bind.go` `Open` → `source.go` + `fetch.go` (HTTPS, AES) → `mp4` + `aac` + `libs/gstreamer` (декодер) → `core/engine` (`stream.go`, `loop.go`) → `daemon/router.go` → `core/speaker` → `speaker/aplay` → `aplay`. Обратно: события `mediad` → `Proxy.Broadcast`. Параллельно спектр: `engine` → `display-spectrum` → порт 9903 |
| **Голос Алисы** | `quasar/mediad/voice.go` → `components/alice-voice/bind.go` → `source.go` → `ogg` + `opus` → `engine` (класс `voice`) |
| **Звонок таймера** | `quasar/mediad/file.go` → `components/system-sounds/bind.go` → `library.go` (GStreamer) → `source.go` (по кругу) → `engine` (класс `alert`) |
| **`openmediad config set …`** | `core/cli/config.go` → `cli/command` `SetConfig` → `core/config/edit.go` → `ctlsock` `reload` → `daemon/control.go` |
| **`openmediad install`** | `core/cli/install.go` → `platform/install` → `platform/initd` (`guard.sh`, `initd.sh`) → `cli/start.go` → `guard.sh` → `platform/stock` `Rerouter` |
| **`openmediad bt pair …`** | `libs/bt-command` → `libs/bluez` → `libs/dbus` → шина D-Bus → BlueZ. **Демон не участвует** |
| **Наушники в демоне** | `components/bt-out` → `libs/bluez` / `libs/dbus` (слежение) → `mixer.go` → `speaker/aplay` → `aplay` в PCM BlueALSA; `daemon/components.go` `attach` → `router.add` |

---

## 7. Где менять

| Хочу изменить… | Смотреть |
|---|---|
| какие компоненты бывают, их класс, зависимости | `components/catalog/catalog.go` (+ `go run ./tools/build -sync-docs`) |
| что войдёт в бинарник | `tools/build`, `components/included/` |
| ключи настроек ядра | `core/config/config.go`, `template.go` |
| ключи настроек компонента | `register.go` этого компонента (структура `Config` и `template`) |
| какой выход слышит каждый класс звука | `[route]` в настройках; логика — `daemon/router.go` |
| тайминги и поля событий «как у mediad» | `core/quasar/mediad/*` (**и золотые тесты**) |
| что пишется в журнал и пояснения | `core/quasar/explain/*` |
| команды терминала | `core/cli/<команда>.go`, порядок — `core/cli/commands.go`; команду компонента — `command.Register` в его пакете |
| как ставится на колонку / откат | `core/platform/install/*`, `core/platform/initd/{guard.sh,initd.sh}` |
| спектр на дисплее | `core/spectrum`, `core/engine/spectrum.go`, `components/display-spectrum` |
| громкость и приглушение | `core/audio/gain.go`, `core/engine`, `[focus]`; наушники — `components/bt-out/{volume,hold}.go` |
| отладочная страница | `components/debug-page/{page.html,server.go}` |
| убрать Bluetooth совсем | удалить `components/bt-out`, `components/bt-in`, `libs/{bluez,dbus,bt-command,aliced-client}`, их `include_*.go` и записи в каталоге; больше нигде Bluetooth не упоминается |

---

## 8. Было → стало

| Было | Стало |
|---|---|
| `internal/app` | `internal/core/daemon` (`shim.go` → `intercept.go` + `aliced.go`) |
| `internal/{engine,audio,spectrum,journal,config,buildinfo}` | `internal/core/{engine,audio,spectrum,journal,config,buildinfo}` |
| `internal/control` | `internal/core/ctlsock` (+ тип `Status`) |
| `internal/registry` | `internal/core/registry`; данные (`catalog.go`) → `internal/components/catalog` |
| `internal/cli` | `internal/core/cli` (+ `command`); `bt.go` → `internal/libs/bt-command` |
| `internal/sink/speaker`, `internal/sink/alsa` | `internal/core/speaker`, `internal/core/speaker/aplay` (`arecord.go` → `components/bt-in/recorder.go`) |
| `internal/quasar/{pb,wire,msg,shim,mediad,explain,observe}` | `internal/core/quasar/…` |
| `internal/quasar/iohub` | `internal/components/display-spectrum` |
| `internal/quasar/control` | `internal/libs/aliced-client` |
| `internal/quasar/capture` | `internal/devtools/capture` |
| `internal/system/{layout,device,stock,iptables,proc,disk,clock,install,initd}` | `internal/core/platform/…` |
| `internal/system/{alsactl,dbus,bluez}` | `internal/libs/{alsactl,dbus,bluez}` |
| `internal/codec/gst` | `internal/libs/gstreamer` |
| `internal/codec/{aac,mp4}` / `{ogg,opus}` / `mp3` | `components/yandex-music/…` / `components/alice-voice/…` / `components/system-sounds/mp3` |
| `internal/source/yandex` / `tts` / `sounds` / `btin` | `components/yandex-music` / `alice-voice` / `system-sounds` / `bt-in` |
| `internal/sink/bluetooth` | `components/bt-out` (`bluetooth.go` разделён на `btout.go`, `transports.go`, `reconnect.go`, `status.go`) |
| `internal/debug`, `internal/debug/pprof` | `components/debug-page`, `components/debug-profiler` |
| `cmd/openmediad/with_*.go` (теги `omd_no_*` и `omd_*`) | `components/included/include_*.go` (единые теги `omd_<имя>`) |
| `cmd/openmediad-dev/btidle.go` | `exp_btidle.go` (одноразовый опыт — по имени видно) |

Секции файла настроек: `[source.yandex]` → `[yandex-music]`, `[source.tts]` → `[alice-voice]`, `[source.sounds]` → `[system-sounds]`,
`[spectrum]` → `[display-spectrum]`, `[debug]` → `[debug-page]`, `[sink.bluetooth]` → `[bt-out]`, `[source.btin]` → `[bt-in]`,
`[sink.speaker]` → `[speaker]`; в `[route]` выход `"bluetooth"` → `"bt-out"`. Совместимости со старым файлом нет: после обновления
`openmediad config reset` (старый файл останется в `.bak`).

---

## 9. Размер бинарника

`linux/arm64`, `-s -w`, замерено `go run ./tools/build -check`.

| Профиль | Размер |
|---|---|
| `core` (только ядро) | 4,06 МиБ |
| `standard` (по умолчанию) | 7,81 МиБ |
| `full` (всё готовое) | 8,56 МиБ |
| инструменты разработчика | 6,88 МиБ |

В `standard` нет пакетов `libs/dbus`, `libs/bluez` и команды `bt` (раньше они попадали в сборку по умолчанию через `cli`). Основной вес даёт
`net/http` + `crypto/tls`, нужные `yandex-music` (HTTPS) и `debug-page` (веб-порт): вместе ≈3,25 МиБ. Код, который не выполняется, оперативку
почти не занимает; если же запускать бинарник из `/tmp` (tmpfs), он целиком в ОЗУ. Настоящие расходы памяти — куча Go, кэш звуков
(`system-sounds`, ≈2,5 МиБ), декодер Opus (≈3 МиБ) и процессы `maind --gst-launch` (≈12 МиБ на трек); их показывает `openmediad status`.
