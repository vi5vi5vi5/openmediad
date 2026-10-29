package btout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/speaker/aplay"
	"openmediad/internal/libs/bluez"
)

// Пробный выход для `openmediad test-sound bt-out`: тон в наушники мимо демона. Регистрируется в init() register.go.
// Пока демон с этим компонентом держит канал в наушники, сигнал не пройдёт: PCM BlueALSA принимает одного клиента.

// openHeadphones — aplay в подключённые наушники (первые, у которых есть PCM вывода BlueALSA).
func openHeadphones(aplayPath string, f audio.Format) (audio.Output, error) {
	b, err := bluez.Open()
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", command.ErrNotDevice, err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	pcm, ok := st.PlaybackPCM("")
	if !ok {
		return nil, errors.New("наушники не подключены (openmediad bt list)")
	}
	p, err := aplay.Start(aplay.Config{Path: aplayPath, Device: bluez.ALSADevice(pcm.Address), Rate: f.Rate, Channels: f.Channels})
	if err != nil {
		return nil, err
	}
	return aplay.NewOutput(p), nil
}
