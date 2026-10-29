# core/config — файл настроек

`/data/opt/openmediad/openmediad.conf`; формат — подмножество TOML со своим парсером (ошибки с номером строки). Ядро: `[general]`,
`[fallback]`, `[route]`, `[focus]`, `[speaker]`. Остальные секции принадлежат компонентам и называются так же, как компонент
(`[yandex-music]`, `[bt-out]` …).

| Файл | Что делает |
|---|---|
| `config.go` | `Config`, `Defaults`, `Load`, `LoadFile` (нет файла — умолчания), `Path`, `Sinks`, `Enabled(имя)`, проверка значений |
| `parse.go` | парсер: `Parse` → `Doc/Section/Key/Value`, `ParseError`, `Quote` |
| `decode.go` | раскладка секции в структуру по тегам `conf:"…"`; `Toggle{Enabled}` — встраивается в настройки компонента |
| `edit.go` | `Get`, `Set` (правка одного ключа с сохранением комментариев), `WriteFile` (атомарно), `ReadText` |
| `template.go` | файл по умолчанию: шаблон ядра + шаблоны компонентов, вошедших в сборку |
