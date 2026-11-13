package daemon

import (
	"context"
	"fmt"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/quasar/alicedclient"
)

// Пульт музыки платформы (control.PlatformRemote). Когда команду не взял ни один плеер движка, а платформа о ней сама не знает (кнопка
// наушников, команда извне), ядро отдаёт её платформе тем же путём, что пункты меню maind --cli: как нажатие кнопки на колонке. Платформа
// сама решает, каким своим плеером управлять, и сама рисует анимации.

// remoteTimeout — сколько ждём aliced: соединение на одну команду, обычно десятки миллисекунд.
const remoteTimeout = 5 * time.Second

// platformRemote — пульт поверх клиента aliced.
type platformRemote struct {
	playing func() bool                          // играет ли сейчас музыка платформы через движок
	ask     func(text string) error              // сказать Алисе фразу, как голосом
	send    func(cmd alicedclient.Command) error // команда плееру платформы, как кнопка колонки
}

// newPlatformRemote — пульт на живой шине.
func newPlatformRemote(playing func() bool) *platformRemote {
	ctl := &alicedclient.Client{}
	with := func(f func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
		defer cancel()
		return f(ctx)
	}
	return &platformRemote{
		playing: playing,
		ask:     func(text string) error { return with(func(ctx context.Context) error { return ctl.Ask(ctx, text) }) },
		send: func(cmd alicedclient.Command) error {
			return with(func(ctx context.Context) error { return ctl.Send(ctx, cmd) })
		},
	}
}

// Command передаёт команду платформе.
func (r *platformRemote) Command(c control.Cmd) error {
	playing := r.playing != nil && r.playing()
	switch c {
	case control.Toggle, control.Pause:
		if playing {
			// пауза — как голосом: локальная команда ставит паузу без анимации на экране, а директива облака — с ней
			return r.ask("поставь на паузу")
		}
		if c == control.Pause {
			return r.send(alicedclient.Pause)
		}
		return r.send(alicedclient.Toggle)
	case control.Resume:
		if playing {
			return nil // уже играет
		}
		return r.send(alicedclient.Toggle)
	case control.Next:
		return r.send(alicedclient.Next)
	case control.Prev:
		return r.send(alicedclient.Prev)
	}
	return fmt.Errorf("команда %q платформе не передаётся", c)
}
