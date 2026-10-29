package daemon

import (
	"context"
	"strings"
	"time"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/observe"
)

// watchAliced следит за диалогом Алисы (только чтение): имена директив ответа уходят компонентам (directiveHandler), а
// журнал (что услышала, думает ли, говорит ли) пишется только если компонент попросил (dialogLogger, например отладочная
// страница): в него попадают распознанные фразы.
func (d *daemon) watchAliced(ctx context.Context, logLines bool) {
	var lastKey string
	var lastAt time.Time
	o := &observe.Observer{
		Targets: []observe.Target{{Name: "aliced", Addr: "127.0.0.1:9875"}},
		OnReply: func(req string, directives []string) {
			// ответ приходит в нескольких состояниях Алисы; номер запроса бывает пустым у некоторых директив
			key := req + " " + strings.Join(directives, ",")
			if key == lastKey && time.Since(lastAt) < 5*time.Second {
				return
			}
			lastKey, lastAt = key, time.Now()
			for _, name := range directives {
				d.directive(name)
			}
		},
		OnLine: func(who, text string) {
			if !logLines {
				return
			}
			lvl, why := journal.Info, ""
			switch {
			case strings.HasPrefix(text, "AliceState"):
				switch {
				case strings.Contains(text, "LISTENING") && strings.Contains(text, "text="):
					why = "Алиса слушает; text — то, что она распознала (final — фраза закончена и пойдёт в облако)."
				case strings.Contains(text, "LISTENING"):
					why = "Алиса услышала «Алиса» и слушает команду."
				case strings.Contains(text, "BUSY"):
					why = "Фраза ушла в облако, Алиса думает; когда ответ придёт, в нём будут директивы (что делать и что сказать)."
				case strings.Contains(text, "SPEAKING"):
					why = "Алиса говорит."
				case strings.Contains(text, "IDLE"):
					why = "Диалог закончен."
				}
			case strings.HasPrefix(text, "Remoting"):
				lvl = journal.Trace
			case strings.Contains(text, "нет соединения"), strings.Contains(text, "закрыто"):
				lvl = journal.Warn
			}
			d.j.Log(lvl, "aliced", text, why, nil)
		},
	}
	go o.Run(ctx)
}

// directive передаёт имя директивы ответа Алисы компонентам, которым это интересно.
func (d *daemon) directive(name string) {
	d.mu.Lock()
	comps := d.comps
	d.mu.Unlock()
	for _, c := range comps {
		if h, ok := c.obj.(directiveHandler); ok {
			h.OnDirective(name)
		}
	}
}
