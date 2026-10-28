package iohub

import (
	"context"
	"errors"
	"time"

	"openmediad/internal/config"
	"openmediad/internal/engine"
	"openmediad/internal/journal"
	"openmediad/internal/registry"
)

// Компонент spectrum: спектр звука для дисплея и подсветки. Движок считает его по тому, что уходит в вывод, а этот
// компонент отправляет кадры сервису iohub (как штатный mediad: 25 кадров в секунду, формат — docs/BUS-PROTOCOL.md, §3).

// Config — секция [spectrum].
type Config struct {
	config.Toggle
	Delay time.Duration `conf:"delay"` // задержка кадров относительно передачи звука в aplay
}

const template = `[spectrum]               # спектр звука для дисплея и подсветки (сервис iohub)
enabled = true
delay = "300ms"          # задержка кадров спектра относительно передачи звука в aplay`

func init() {
	registry.Register(registry.Component{
		Name:     "spectrum",
		Defaults: func() any { return &Config{Toggle: config.Toggle{Enabled: true}, Delay: DefaultDelay} },
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			eng, ok := registry.Use[*engine.Engine](env, "engine")
			if !ok {
				return nil, errors.New("spectrum: нет движка")
			}
			j, _ := registry.Use[*journal.Journal](env, "journal")
			cli := &Client{Logf: env.Logf("engine")}
			snd := NewSender(cli, cfg.(*Config).Delay, j)
			eng.OnSpectrum = snd.Add
			return &module{cli: cli, snd: snd}, nil
		},
	})
}

type module struct {
	cli *Client
	snd *Sender
}

func (m *module) Start(ctx context.Context) error {
	go m.cli.Run(ctx)
	go m.snd.Run(ctx)
	return nil
}

func (m *module) Stop() {}
