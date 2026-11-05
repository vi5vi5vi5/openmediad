package daemon

import (
	"context"
	"fmt"

	"openmediad/internal/core/control"
	"openmediad/internal/core/journal"
)

// Плееры движка и ядро команд (core/control). Источники — кнопки колонки (buttons.go), кнопки наушников (bt-out), голос и ответ облака
// (aliced.go), команды платформы виртуальному плееру (ниже) — говорят Hub «пауза, откуда-то»; чей сейчас звук и не повтор ли это, решает он.

// startPlayers запускает то, что нужно плеерам движка, если они есть в сборке: уступку звука между ними и Яндекс.Музыкой и приём команд, которые
// платформа адресует виртуальному плееру движка.
func (d *daemon) startPlayers(ctx context.Context) {
	if d.hub.Len() == 0 {
		return
	}
	d.m.OnVirtualCommand(d.virtualCommand)
	go d.hub.Run(ctx)
}

// virtualCommand — платформа адресовала команду плееру движка, которого считает своим (усыновлённая сессия): пауза или освобождение ставят его
// на паузу, play продолжает.
func (d *daemon) virtualCommand(name string) {
	cmd, ok := control.ParseVirtual(name)
	if !ok {
		d.j.Log(journal.Info, "control", fmt.Sprintf("платформа → плеер движка: непонятная команда %q", name), "", nil)
		return
	}
	d.hub.Do(cmd, control.Virtual)
}

// controlHandled пишет в журнал, что плеер движка выполнил команду.
func (d *daemon) controlHandled(c control.Cmd, from control.Origin, player string) {
	d.j.Log(journal.Info, "control", fmt.Sprintf("%s (%s): выполнено плеером %s", c, from, player),
		"Команду плееру отдал источник (кнопки колонки, кнопки наушников, голос, ответ облака, платформа); музыку играет движок, а платформа про этот плеер не знает, поэтому выполнил его компонент.", nil)
}
