# Сторонний код

Всё лежит в `vendor/` (сборка идёт с `-mod=vendor`, сеть при сборке не нужна). Версии и хэши модулей
зафиксированы в `go.mod`/`go.sum`; `go mod verify` должен отвечать «all modules verified».

| Модуль | Версия | Зачем | Лицензия |
|---|---|---|---|
| github.com/tphakala/go-opus | v1.3.0 | декодер Opus (CELT/SILK/hybrid) на чистом Go, без CGO | BSD-3-Clause |
| github.com/tphakala/simd | v1.14.0 | SIMD-ядра для go-opus (Go + ассемблер) | MIT |
| golang.org/x/sys | v0.49.0 | определение возможностей CPU (пакет cpu) | BSD-3-Clause (+PATENTS) |
| github.com/ulikunitz/xz | v0.5.17 | декодер xz для пакетов .deb (`internal/libs/debpkg`, компонент `spotify-soloist`) | BSD-3-Clause |

## Почему именно так
- Декодер выбран по сверке на реальных чанках голоса Алисы: docs/research/HISTORY.md, раздел 13 (SNR 74 дБ к эталону колонки,
  одинаково на ПК и на arm64, ≈25 мс на секунду звука на колонке).
- Статический аудит импортов (без запуска): нет `net`, `os/exec`, `plugin`; `os`/`syscall` — только в определении
  возможностей CPU; `init()` — таблицы и определение CPU. Полного ручного аудита кода и ассемблера НЕТ.
- Включены только пакеты, которые реально импортируются (`go mod vendor`), всего ≈2 МБ.

## Не входит в репозиторий (скачивается при установке компонента `spotify-soloist`)

| Что | Откуда | Лицензия | Как |
|---|---|---|---|
| Spotify Soloist | CDN Spotify (`soloist-builds.spotifycdn.com`) | закрытая, условия Spotify; **распространять нельзя** | `openmediad spotify setup` на колонке; проект его не содержит |
| glibc 2.41 (`ld-linux`, `libc`, `libm` …) из пакета `libc6_2.41-12+deb13u4_armhf.deb` | зеркало и снимок архива Debian | LGPL-2.1+ | скачивается при установке, sha256 пакета зафиксирован в коде; лежит отдельными файлами рядом с Soloist, заменяема пользователем |
| заголовки PulseAudio 17 (только при пересборке шима) | пакет `libpulse-dev` Debian | LGPL-2.1+ | не входят в сборку openmediad; шим написан по публичному API, своего кода PulseAudio в нём нет |

Шим `libpulse.so.0` — собственный код проекта (MIT, `internal/components/spotify-soloist/shim/`), собранный `zig cc`; готовый файл лежит в
`internal/components/spotify-soloist/assets/` вместе с хэшем.

## Контроль целостности
`vendor.sha256` — построчные SHA-256 файлов `vendor/`; хэш этого списка при вендоринге:

```
dd4fb48bafdfdf9f8ae3bbef9bb439b74db1d29c8b79cc1f9aaa55d8f36bfc6e  vendor.sha256
```

Проверка (из корня проекта, работает на любой ОС):

```sh
go run ./tools/build -verify      # хэши файлов vendor/ против vendor.sha256 и go mod verify
```

Список понимает оба формата `sha256sum`: «хэш  путь» (Linux) и «хэш *путь» (Git Bash на Windows).

## Обновление
Менять версию только осознанно: `go get модуль@версия && go mod tidy && go mod vendor`, затем повторить сверку
`tools/celtcheck` на реальных чанках, пересчитать `vendor.sha256`
(`cd vendor && find . -type f | LC_ALL=C sort | xargs sha256sum > ../vendor.sha256`) и обновить эту таблицу.
