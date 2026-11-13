# core/ctlsock — сокет «терминал ↔ демон»

Unix-сокет `/tmp/openmediad/ctl.sock`; запрос и ответ — строки JSON, без `net/http`.

* `control.go` — `Server` (в демоне), `Call` и `Stream` (в терминале), `Request{Cmd: status | logs | follow | reload | control | players | events | ask}`, `RunDir`, `SocketPath`, `ErrNotRunning`.
* `status.go` — тип ответа `status` (`Status`, `ComponentStatus`, `StockUse` — что ещё делает штатный mediad): лежит здесь, чтобы терминальные команды не зависели от всего демона.
* `api.go` — типы управления для своих программ: `ControlResult`, `Playing`, `Event` (описание — [docs/API.md](../../../docs/API.md)).
