# wsclient — клиент WebSocket

Минимальный клиент RFC 6455 поверх обычного TCP: рукопожатие (проверка `Sec-WebSocket-Accept`), текстовые и двоичные сообщения,
склейка фрагментов, ответ на ping, закрытие как `io.EOF`. Без сжатия и без TLS: нужен для локальных соединений.

```go
c, err := wsclient.Dial(ctx, "127.0.0.1:9090", "/")
c.WriteText(`{"type":"command","command":"pause"}`)
msg, err := c.ReadMessage()
```

Читает одна горутина (`ReadMessage`); писать (`WriteText`) можно из нескольких. Сообщения больше 4 МБ отвергаются.

Кому нужна: `spotify-soloist` (WebSocket API Soloist на `127.0.0.1`). Тесты — `wsclient_test.go` (поддельный сервер в тесте).
