# quasar/pb — protobuf без схем

Разбор и сборка protobuf по номерам полей (схем шины в прошивке нет, `maind` собран в lite-режиме). Разбор не выделяет память: `Field.B`
ссылается на исходный буфер.

* `pb.go` — `Reader`, `Find` / `Bytes` / `Uint` / `Str`, сборка (`AppendVarint` / `AppendUint` / `AppendBytes` / `AppendField`).
* `dump.go` — `Dump`: дерево полей одной строкой для отладки (тип угадывается: строка, вложенное сообщение, hex).
