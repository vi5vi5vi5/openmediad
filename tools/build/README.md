# tools/build — сборка

Работает на Windows, Linux и macOS (make и sh не нужны). Тег сборки компонента `omd_<имя>` инструмент подбирает сам.

```sh
go run ./tools/build                       # профиль standard → dist/openmediad (linux/arm64)
go run ./tools/build -profile core         # только ядро (прозрачный посредник)
go run ./tools/build -profile full         # все готовые компоненты
go run ./tools/build -with bt-out          # профиль + компонент
go run ./tools/build -without debug-page   # профиль − компонент
go run ./tools/build -list                 # компоненты с паспортами
go run ./tools/build -check                # собираются ли core / standard / full и инструменты разработчика
go run ./tools/build -dev                  # openmediad-dev
go run ./tools/build -deploy 192.168.1.12  # собрать и залить на колонку (ssh, порт 2222) в /tmp/openmediad/
go run ./tools/build -verify               # целостность vendor/
go run ./tools/build -sync-docs            # переписать docs/COMPONENTS.md и паспорта в README компонентов
```

Если выбранному компоненту нужен другой (`debug-profiler` → `debug-page`), инструмент добавит его сам и скажет об этом.
