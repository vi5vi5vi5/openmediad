# libs/bt-command — команда `openmediad bt`

`bt list | scan | pair | connect | disconnect | forget | use` — управление Bluetooth колонки **напрямую** через D-Bus, демон не участвует.
`bt use АДРЕС` пишет настройку `[bt-out] device`.

Пакет регистрирует команду в `init()` (`command.Register`) и подключается компонентами `bt-out` и `bt-in`. Go подключает пакет один раз,
поэтому команда есть, если в сборку вошёл хотя бы один из них, и пропадает вместе с ними; `libs/bluez` и `libs/dbus` тоже.
