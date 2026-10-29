# core — ядро

Всё, без чего движок не существует и что всегда входит в бинарник. Ядро не знает ни одного компонента по имени: всё, что нужно
от компонентов, они приносят сами через точки расширения (`StatusLine`, `OnDirective`, `command.Register`,
`command.RegisterTestOutput`).

| Папка | Зачем |
|---|---|
| [`daemon/`](daemon/README.md) | запуск и жизнь демона: соединяет все остальные части |
| [`engine/`](engine/README.md) | потоки звука, классы (музыка / голос / звонок), громкость, позиция по часам |
| [`audio/`](audio/README.md) | формат PCM, кольцевой буфер, громкость, нарастание; договор `Output` |
| [`spectrum/`](spectrum/README.md) | математика спектра (БПФ): её вызывает движок |
| [`speaker/`](speaker/README.md) | выход «динамики колонки» (всегда есть) и запуск системного `aplay` |
| [`quasar/`](quasar/README.md) | язык шины Quasar и эмуляция штатного плеера `mediad` |
| [`registry/`](registry/README.md) | механизм компонентов: паспорт, регистрация, окружение |
| [`config/`](config/README.md) | файл настроек |
| [`journal/`](journal/README.md) | журнал |
| [`ctlsock/`](ctlsock/README.md) | сокет «терминал ↔ демон» и тип ответа `status` |
| [`cli/`](cli/README.md) | команды терминала (`status`, `config`, `install` …) |
| [`platform/`](platform/README.md) | колонка как система: пути, установка, страж, iptables, /proc |
| [`buildinfo/`](buildinfo/README.md) | версия, дата, коммит и теги сборки |
