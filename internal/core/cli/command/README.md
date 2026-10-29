# cli/command — как устроена команда и как добавить свою

Маленький пакет, отдельный от `cli`, чтобы команду могли принести не только команды ядра, но и компоненты с библиотеками.

* `command.go` — `Command`, `Ctx`, `UsageError`; **`Register(группа, команда)`** (из `init()`), `All`, `Find`, `Flags`, `Parse`.
* `testoutput.go` — **`RegisterTestOutput(имя, ...)`**: компонент-выход добавляет себя в `openmediad test-sound ИМЯ`.
* `config.go` — `SetConfig` / `ReloadDaemon`: правка ключа настроек и просьба демону перечитать (общий код `config set` и `bt use`).

Пример: команда `bt` приходит из [`libs/bt-command`](../../../libs/bt-command/README.md) и есть в справке, только если собран `bt-out` или `bt-in`.
