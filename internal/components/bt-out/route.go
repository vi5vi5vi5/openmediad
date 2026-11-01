package btout

import (
	"context"

	"openmediad/internal/core/control"
	"openmediad/internal/libs/aliced-client"
)

// Куда идёт нажатие кнопки на наушниках. Сначала его получает плеер движка (Spotify) через ядро: если музыка играет он, команду выполняет он,
// и платформа о нажатии не узнаёт. Если плееру движка команда не принадлежит (играет Яндекс.Музыка или ничего), нажатие уходит платформе,
// как если бы нажали на колонке.
type buttonRoute struct {
	Hub     *control.Hub                     // ядро: плееры движка (может быть nil — тогда всё платформе)
	Playing func() bool                      // играет ли музыка Яндекса через движок
	Ask     func(text string) error          // сказать Алисе фразу, как голосом
	Send    func(alicedclient.Command) error // команда плееру платформы, как кнопка колонки
	Logf    func(string, ...any)
}

// playerCommand — какая команда плееру соответствует кнопке.
func playerCommand(b Button) (control.Cmd, bool) {
	switch b {
	case ButtonPlayPause:
		return control.Toggle, true
	case ButtonStop:
		return control.Pause, true
	case ButtonNext:
		return control.Next, true
	case ButtonPrev:
		return control.Prev, true
	}
	return "", false
}

// press обрабатывает нажатие.
func (r *buttonRoute) press(b Button) {
	if cmd, ok := playerCommand(b); ok && r.Hub.Do(cmd, control.Headphones) {
		return // журнал об этом пишет ядро
	}
	var err error
	switch {
	case (b == ButtonPlayPause || b == ButtonStop) && r.Playing != nil && r.Playing():
		// пауза — как голосом: локальная команда ставит паузу без анимации на экране, а директива облака — с ней
		err = r.Ask("поставь на паузу")
	default:
		cmd := map[Button]alicedclient.Command{ButtonPlayPause: alicedclient.Toggle, ButtonStop: alicedclient.Pause,
			ButtonNext: alicedclient.Next, ButtonPrev: alicedclient.Prev}[b]
		err = r.Send(cmd)
	}
	if err != nil {
		r.Logf("кнопка наушников «%s»: платформе не передана: %v", b, err)
		return
	}
	r.Logf("кнопка наушников «%s» → платформе", b)
}

// newButtonRoute — маршрут нажатий на живой шине: диалог с Алисой и команды плееру платформы идут через aliced.
func newButtonRoute(hub *control.Hub, playing func() bool, logf func(string, ...any)) *buttonRoute {
	ctl := &alicedclient.Client{}
	return &buttonRoute{
		Hub: hub, Playing: playing, Logf: logf,
		Ask:  func(text string) error { return ctl.Ask(context.Background(), text) },
		Send: func(c alicedclient.Command) error { return ctl.Send(context.Background(), c) },
	}
}
