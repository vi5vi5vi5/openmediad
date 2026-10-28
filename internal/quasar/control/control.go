// Package control — команды плееру от имени клиента aliced, как пункты maind --cli: пауза, продолжить, следующий и
// предыдущий трек (PlaybackControlCapability, quasar messaging/PROTOCOL.md §4.4). Платформа выполняет их так же, как
// нажатие кнопки: сама решает, каким плеером управлять, и сама шлёт команды плееру (через шим — движку).
package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"time"

	"openmediad/internal/quasar/wire"
)

// DefaultAddr — aliced на колонке.
const DefaultAddr = "127.0.0.1:9875"

// endpointName — имя клиента при регистрации: как у maind --cli (другие имена не проверялись).
const endpointName = "cli"

// Command — команда плееру.
type Command int

const (
	Toggle Command = iota + 1 // пауза или продолжить
	Pause
	Next
	Prev
)

func (c Command) String() string {
	switch c {
	case Toggle:
		return "пауза/продолжить"
	case Pause:
		return "пауза"
	case Next:
		return "дальше"
	case Prev:
		return "назад"
	}
	return fmt.Sprintf("команда %d", int(c))
}

// Client отправляет команды. Нулевой Client годен: aliced по адресу DefaultAddr, источник «Keyboard».
type Client struct {
	Addr   string // aliced; пусто — DefaultAddr
	Source string // метка источника (видна в директивах платформы: playback_next.<Source>); пусто — «Keyboard»
	// Dial (необязательный) — подключение; по умолчанию TCP.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

// Send открывает соединение с aliced, ждёт его приветствия, регистрируется (как maind --cli) и отправляет команду.
// Соединение — на одну команду: кнопки нажимают редко, а постоянное пришлось бы всё время вычитывать (aliced
// рассылает каждому клиенту состояние Алисы и прочее).
func (c *Client) Send(ctx context.Context, cmd Command) error {
	source := c.Source
	if source == "" {
		source = "Keyboard"
	}
	var frame []byte
	switch id := newID(); cmd {
	case Toggle:
		frame = wire.TogglePlayback(id, source)
	case Pause:
		frame = wire.PausePlayback(id, source)
	case Next:
		frame = wire.NextTrack(id, source)
	case Prev:
		frame = wire.PrevTrack(id, source)
	default:
		return fmt.Errorf("control: неизвестная команда %d", int(cmd))
	}
	addr := c.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dial := c.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}
	}
	conn, err := dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("aliced: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	// приветствие (Handshake): команда, пришедшая раньше, чем aliced принял клиента, могла бы пропасть
	fr := wire.NewFrameReader(conn)
	for {
		f, err := fr.Next()
		if err != nil {
			return fmt.Errorf("aliced: нет приветствия: %w", err)
		}
		if m, err := wire.Unwrap(f); err == nil && m.Type == wire.TypeHandshake {
			break
		}
	}
	out := wire.AppendFrame(nil, wire.RegisterEndpoint(newID(), endpointName))
	out = wire.AppendFrame(out, frame)
	if _, err := conn.Write(out); err != nil {
		return fmt.Errorf("aliced: %w", err)
	}
	// дать aliced прочитать команду до того, как он увидит закрытие: дочитываем его рассылку 150 мс (или пока он сам
	// не закроет соединение — тогда команда уже прочитана)
	conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	for {
		if _, err := fr.Next(); err != nil {
			return nil
		}
	}
}

// newID — случайный UUID (версия 4) для запроса и регистрации.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
