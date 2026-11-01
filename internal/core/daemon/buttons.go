package daemon

import (
	"context"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/buttons"
)

// watchButtons следит за кнопкой Алисы и жестами по колонке и сообщает компонентам (buttonHandler), что это было. Лог платформы читается
// только если такой компонент есть.
func (d *daemon) watchButtons(ctx context.Context) {
	if len(d.buttonHandlers()) == 0 {
		return
	}
	w := &buttons.Watcher{Path: d.platformLog, OnAction: d.button}
	go w.Run(ctx)
}

func (d *daemon) buttonHandlers() []buttonHandler {
	d.mu.Lock()
	defer d.mu.Unlock()
	var hs []buttonHandler
	for _, c := range d.comps {
		if h, ok := c.obj.(buttonHandler); ok && c.err == nil {
			hs = append(hs, h)
		}
	}
	return hs
}

// button отдаёт действие кнопок компонентам по очереди; тот, кто его выполнил, останавливает обход: два компонента не должны
// ответить на одно нажатие.
func (d *daemon) button(a buttons.Action) {
	for _, h := range d.buttonHandlers() {
		if h.OnButton(a) {
			d.j.Log(journal.Info, "buttons", "колонка: "+a.String()+" — выполнено компонентом",
				"Платформа записала нажатие или жест в свой лог; компонент выполнил его в своём плеере (платформа к нему не имеет отношения).", nil)
			return
		}
	}
}
