package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openmediad/internal/core/control"
	"openmediad/internal/core/ctlsock"
)

// Управление через сокет команд (docs/API.md): команды плееру, «кто играет», события, фраза Алисе. Это тот же путь, что у кнопок
// наушников: один вызов ядра Hub.Command, а чей сейчас звук, решает оно. Свои программы (скрипт на колонке, `ssh колонка openmediad player
// next`) получают всё это без пересборки движка.

// events — рассылка событий подписчикам сокета. Медленный подписчик события теряет, но никого не задерживает.
type events struct {
	mu   sync.Mutex
	subs map[int]chan ctlsock.Event
	next int
}

func (b *events) publish(e ctlsock.Event) {
	e.AtMs = time.Now().UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *events) subscribe() (<-chan ctlsock.Event, func()) {
	ch := make(chan ctlsock.Event, 32)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[int]chan ctlsock.Event{}
	}
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// api — состояние управления через сокет в демоне.
type api struct {
	bus    events
	dialog atomic.Bool // идёт диалог с Алисой
	// ask говорит Алисе фразу, как голосом (подменяется в тестах).
	ask func(text string) error
}

// playerEvent — событие ядра команд уходит подписчикам сокета.
func (d *daemon) playerEvent(e control.Event) {
	d.api.bus.publish(ctlsock.Event{Type: ctlsock.EventPlayer, Action: string(e.Cmd), From: string(e.From), Player: e.Player,
		PlatformDisplay: e.PlatformDisplay, PlatformRing: e.PlatformRing})
}

var apiActions = map[string]control.Cmd{
	ctlsock.ActionPause: control.Pause, ctlsock.ActionResume: control.Resume, ctlsock.ActionToggle: control.Toggle,
	ctlsock.ActionNext: control.Next, ctlsock.ActionPrev: control.Prev,
}

// apiControl выполняет команду плееру, пришедшую извне.
func (d *daemon) apiControl(action string) (ctlsock.ControlResult, error) {
	cmd, ok := apiActions[action]
	if !ok {
		return ctlsock.ControlResult{}, fmt.Errorf("неизвестное действие %q: pause, resume, toggle, next или prev", action)
	}
	by, err := d.hub.Command(cmd, control.API)
	if err != nil {
		return ctlsock.ControlResult{}, fmt.Errorf("платформе команда не передана: %w", err)
	}
	if by == "" {
		return ctlsock.ControlResult{}, errors.New("команду некому выполнить: ни один плеер движка её не взял, а пульта платформы нет")
	}
	return ctlsock.ControlResult{Action: action, By: by}, nil
}

// apiPlaying — кто играет сейчас.
func (d *daemon) apiPlaying() ctlsock.Playing {
	out := ctlsock.Playing{Owner: d.hub.Owner(), Dialog: d.api.dialog.Load(), Players: []ctlsock.PlayerState{}, Platform: []ctlsock.PlatformTrack{}}
	for _, p := range d.hub.Players() {
		st := p.State
		out.Players = append(out.Players, ctlsock.PlayerState{Name: p.Name, Ready: st.Ready, Playing: st.Playing, Sounding: st.Sounding,
			Paused: st.Paused, Resumable: st.Resumable, Track: st.Track, Title: st.Title, NearEnd: st.NearEnd})
	}
	d.mu.Lock()
	ready := d.comps != nil // пока компоненты подключаются, автомат собирается: его поля не читаем
	d.mu.Unlock()
	if ready {
		out.PlatformPlaying = d.m.Playing()
		for _, p := range d.m.Snapshot() {
			out.Platform = append(out.Platform, ctlsock.PlatformTrack{State: p.State, Title: p.Title, PosMs: p.PosMs, DurMs: p.DurMs})
		}
	}
	return out
}

// apiLink включает музыку по ссылке: ядро отдаёт её плееру, чей это домен (control.Hub.PlayLink).
func (d *daemon) apiLink(ctx context.Context, link string) (ctlsock.LinkResult, error) {
	ctx, cancel := context.WithTimeout(ctx, linkTimeout)
	defer cancel()
	player, title, err := d.hub.PlayLink(ctx, link)
	if err != nil {
		return ctlsock.LinkResult{}, err
	}
	return ctlsock.LinkResult{Player: player, Title: title}, nil
}

// linkTimeout — сколько плеер может разбираться со ссылкой (узнать по сети, что это, и запустить).
const linkTimeout = 20 * time.Second

// apiAsk говорит Алисе фразу, как голосом: она ответит и выполнит её, как обычную просьбу.
func (d *daemon) apiAsk(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("пустая фраза")
	}
	if len(text) > 512 {
		return errors.New("фраза длиннее 512 байт")
	}
	if d.api.ask == nil {
		return errors.New("нет связи с Алисой")
	}
	return d.api.ask(text)
}

// apiEvents шлёт подписчику события, пока он не отключится.
func (d *daemon) apiEvents(ctx context.Context, send func(any) error) error {
	ch, unsub := d.api.bus.subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-ch:
			if err := send(e); err != nil {
				return nil
			}
		}
	}
}
