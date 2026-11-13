package daemon

import "openmediad/internal/core/ctlsock"

// Диалог с Алисой глазами ядра. Платформа сообщает плееру, что диалог занял аудиофокус (Алиса слушает, думает или говорит) и что отдал его.
// Музыку на это время приглушает ядро (Machine.MusicGain → класс «музыка» движка), а здесь об этом узнают компоненты.

// focusChanged вызывает автомат при начале (on) и конце диалога. Вызывается из разбора кадра шины: только быстрое.
func (d *daemon) focusChanged(on bool) {
	d.api.dialog.Store(on)
	d.api.bus.publish(ctlsock.Event{Type: ctlsock.EventDialog, On: on})
	d.mu.Lock()
	comps := d.comps
	d.mu.Unlock()
	for _, c := range comps {
		if h, ok := c.obj.(focusHandler); ok && c.err == nil {
			h.OnFocus(on)
		}
	}
}
