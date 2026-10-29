# core/ctlsock — сокет «терминал ↔ демон»

Unix-сокет `/tmp/openmediad/ctl.sock`; запрос и ответ — строки JSON, без `net/http`.

* `control.go` — `Server` (в демоне), `Call` и `Stream` (в терминале), `Request{Cmd: status | logs | follow | reload}`, `RunDir`, `SocketPath`, `ErrNotRunning`.
* `status.go` — тип ответа `status` (`Status`, `ComponentStatus`): лежит здесь, чтобы терминальные команды не зависели от всего демона.
