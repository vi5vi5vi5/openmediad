package yandex

import (
	"errors"

	"openmediad/internal/config"
	"openmediad/internal/engine"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/registry"
)

// Config — секция [source.yandex].
type Config struct {
	config.Toggle
}

const template = `[source.yandex]          # музыка Яндекса: openmediad играет её сам (выключено — играет штатный mediad)
enabled = true`

func init() {
	registry.Register(registry.Component{
		Name:     "yandex",
		Defaults: func() any { return &Config{Toggle: config.Toggle{Enabled: true}} },
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			eng, ok1 := registry.Use[*engine.Engine](env, "engine")
			m, ok2 := registry.Use[*mediad.Machine](env, "machine")
			if !ok1 || !ok2 {
				return nil, errors.New("yandex: нет движка или автомата mediad")
			}
			b := &Backend{Engine: eng, Events: m, Logf: env.Logf("music")}
			b.Warm() // сертификаты читаются при запуске, а не на первой команде (иначе задержится кадр шины)
			return b, nil
		},
	})
}

// DebugState — треки для отладочной страницы.
func (b *Backend) DebugState() any { return b.Snapshot() }
