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
		OnPhrase: func(req, text string, fast bool) { d.phrase(req, text, fast, logLines) },
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

// phrase отдаёт окончательную фразу компонентам-перехватчикам по очереди: тот, кто взял её, выполняет её сам, а ответ облака голосом
// гасится (шлюз). Саму фразу в журнал пишет только режим диалога (logLines): фразы приватны.
func (d *daemon) phrase(req, text string, fast, logLines bool) {
	type taker struct {
		name string
		h    phraseInterceptor
	}
	var takers []taker
	d.mu.Lock()
	gate := d.gate
	for _, c := range d.comps {
		if h, ok := c.obj.(phraseInterceptor); ok && c.err == nil {
			takers = append(takers, taker{c.name, h})
		}
	}
	d.mu.Unlock()
	taken := false
	defer func() {
		if !taken && gate != nil {
			gate.Clear() // ответ на эту фразу должен прозвучать, даже если прошлую перехватили секунду назад
		}
	}()
	for _, t := range takers {
		if !t.h.InterceptPhrase(req, text, fast) {
			continue
		}
		taken = true
		if gate != nil && !fast { // ответ быстрой команды беззвучный и приходит через 30 мс: гасить в нём нечего
			gate.Swallow()
		}
		var detail map[string]any
		if logLines {
			detail = map[string]any{"фраза": text}
		}
		d.j.Log(journal.Info, "aliced", "фраза перехвачена компонентом "+t.name,
			"Компонент выполнил её сам; ответ облака голосом гасится, чтобы Алиса не отвечала поверх.", detail)
		return
	}
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
