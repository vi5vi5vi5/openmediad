# core/cli — команды терминала

`openmediad <команда>` — короткоживущий процесс. По файлу на команду. Новая команда ядра: свой файл + строка в `commands.go`.
Команды, которые приносят **не** ядро, регистрируются через [`command`](command/README.md) (так появляется `bt`).

| Файл | Что делает |
|---|---|
| `cli.go` | `Main`, коды выхода, разбор имени команды |
| `commands.go` | группы и порядок команд ядра в справке |
| `help.go` | `help`, `version`, шапка справки (версия, теги, компоненты) |
| `status.go`, `system_status.go` | `status`: что играет сам движок, соединения, память, строки компонентов; установка, страж, iptables |
| `components.go` | `components [-all] [-v]`: паспорта, в сборке ли, включён ли, работает ли |
| `config.go`, `editor.go` | `config` (редактор) и `check / get / set / show / path / reset` |
| `logs.go` | `logs [-f] [-level] [-why] [-json]` |
| `run.go` | `run` — сам демон (для стража) |
| `start.go`, `guard.go` | `start` / `stop` / `restart` / `reroute`; помощники: запись и запуск стража, pid, ожидание демона |
| `install.go` | `install`, `uninstall`, `enable`, `disable` |
| `doctor.go` | `doctor`: модель, права, iptables, установка, правило, порты, место, память и требования вошедших компонентов |
| `reboot.go`, `testsound.go`, `ask.go` | `reboot`; `test-sound [ВЫХОД]` — тон мимо демона; вопрос «да/нет» |
| `detach_*.go` | `Setsid`: страж переживает выход из ssh |
