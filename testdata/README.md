# testdata

Обезличенные записи трафика шины (`*.omdf`) для золотых тестов эмуляции mediad.

Записи с колонки содержат идентификаторы аккаунта, подписанные ссылки и ключи, поэтому как есть сюда не кладутся.
Обезличивание (строки той же длины, кадры остаются корректными):

```sh
go run ./tools/build -dev -host -o dist/openmediad-dev
dist/openmediad-dev sanitize captures/music-session.pcap testdata/music-session.omdf
```

Тесты ищут записи здесь и в каталоге из переменной `OMD_CAPTURES`; без записей золотые тесты пропускаются.
