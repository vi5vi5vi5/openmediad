# components — компоненты

Опциональные возможности движка. Каждая — в своей папке, с **паспортом** (в `README.md` папки и в [docs/COMPONENTS.md](../../docs/COMPONENTS.md)):
класс (необходимый / штатный / вспомогательный / необязательный), входит ли в сборку по умолчанию, что требует от прошивки и от
других компонентов, что будет, если его убрать.

Любой компонент можно не включать в сборку, и колонка не сломается: то, что он делал, играет штатный `mediad`.

| Папка | Название | Класс | По умолчанию |
|---|---|---|---|
| [`yandex-music/`](yandex-music/README.md) | Музыка Яндекса | необходимый | да |
| [`alice-voice/`](alice-voice/README.md) | Голос Алисы | необходимый | да |
| [`system-sounds/`](system-sounds/README.md) | Звонки и системные звуки | штатный | да |
| [`display-spectrum/`](display-spectrum/README.md) | Спектр для дисплея и подсветки | штатный | да |
| [`debug-page/`](debug-page/README.md) | Отладочная веб-страница | вспомогательный | да |
| [`debug-profiler/`](debug-profiler/README.md) | Профилировщик Go | вспомогательный | нет |
| [`bt-out/`](bt-out/README.md) | Bluetooth-наушники: вывод звука | необязательный | нет |
| [`bt-in/`](bt-in/README.md) | Bluetooth: звук с телефона | необязательный | нет |

Служебные папки (это не компоненты): [`catalog/`](catalog/README.md) — таблица паспортов; [`included/`](included/README.md) — что войдёт в бинарник.

**Новый компонент:** папка `internal/components/<имя>/` с `register.go` (в `init()` — `registry.Register`) и `README.md` с маркерами паспорта →
запись в `catalog/catalog.go` → файл `included/include_<имя>.go` → `go run ./tools/build -sync-docs` → тесты. Имя — одно на всё: папка,
секция настроек, `-with`/`-without`; тег сборки — `omd_` и то же имя с `_` вместо `-`.
