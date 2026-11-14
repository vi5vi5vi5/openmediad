# wsclient — клиент WebSocket

Минимальный клиент RFC 6455: рукопожатие (проверка `Sec-WebSocket-Accept`), текстовые и двоичные сообщения, склейка фрагментов, ответ на ping,
закрытие как `io.EOF`. Без сжатия.

```go
// локальное соединение без TLS
c, err := wsclient.Dial(ctx, "127.0.0.1:9090", "/")
c.WriteText(`{"type":"command","command":"pause"}`)
msg, err := c.ReadMessage()

// сервер в сети: wss://, свои заголовки, срок записи, пропуск двоичного потока
c, err := wsclient.DialURL(ctx, "wss://example.com/ws", &wsclient.Options{TLS: cfg, WriteTimeout: 5 * time.Second, SkipBinary: true})
c.WriteBinary(frame)
binary, msg, err := c.Read()
```

`DialURL` принимает `ws://` и `wss://`; имя хоста должно быть в ASCII (домен с национальными буквами — в виде `xn--…`). `Options`:

| Поле | Зачем |
|---|---|
| `Header` | заголовки рукопожатия (например, `Cookie`) |
| `TLS` | настройки TLS для `wss://` (корневые сертификаты) |
| `WriteTimeout` | срок одной записи: застрявшее соединение не держит пишущего |
| `SkipBinary` | двоичные сообщения читаются из сети и выбрасываются, не попадая в память; сколько выброшено — `Skipped()` |
| `MaxMessage` | предел принимаемого сообщения (по умолчанию 4 МБ) |

Читает одна горутина (`ReadMessage`, `Read`); писать (`WriteText`, `WriteBinary`) можно из нескольких.

Кому нужна: `spotify-soloist` (WebSocket API Soloist на `127.0.0.1`), `meetup` (сервер конференции: чат текстом, звук двоичными кадрами, чужое
видео пропускается). Тесты — `wsclient_test.go` (поддельный сервер в тесте, TLS — `httptest`).
