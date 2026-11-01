package daemon

import (
	"context"
	"fmt"

	"openmediad/internal/core/control"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/buttons"
)

// watchButtons следит за кнопкой Алисы и жестами по колонке и отдаёт их в Hub как команды плееру. Лог платформы читается только если в сборке
// есть плеер движка, которому они могут пригодиться.
func (d *daemon) watchButtons(ctx context.Context) {
	if d.hub.Len() == 0 {
		return
	}
	w := &buttons.Watcher{Path: d.platformLog, OnAction: d.button}
	go w.Run(ctx)
}

// button переводит действие кнопок колонки в команду плееру. Не взял никто — играет Яндекс.Музыка или ничего: платформа справится сама.
func (d *daemon) button(a buttons.Action) {
	cmd := map[buttons.Action]control.Cmd{buttons.Toggle: control.Toggle, buttons.Next: control.Next, buttons.Prev: control.Prev}[a]
	if cmd == "" {
		return
	}
	d.hub.Do(cmd, control.Speaker)
}

// controlHandled пишет в журнал, что плеер движка выполнил команду.
func (d *daemon) controlHandled(c control.Cmd, from control.Origin, player string) {
	d.j.Log(journal.Info, "control", fmt.Sprintf("%s (%s): выполнено плеером %s", c, from, player),
		"Команду плееру отдал источник (кнопки колонки, кнопки наушников); музыку играет движок, а платформа про этот плеер не знает, поэтому выполнил его компонент.", nil)
}
