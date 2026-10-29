# yandex-music/mp4 — таблица сэмплов MP4

`Index` — таблица сэмплов AAC из `moov` обычного (не фрагментированного) MP4: по ней перемотка — это HTTP-запрос Range на нужный кадр.

* `mp4.go` — `ReadMoov`, `SampleAtMs`, `StartMs`.
* `tables.go` — разбор `stsz` / `stco` / `co64` / `stsc` / `stts`.
* [`mp4test/`](mp4test/README.md) — синтетические MP4 для тестов.
