package yandexmusic

import (
	"errors"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
)

// Config — секция [yandex-music].
type Config struct {
	config.Toggle
}

const template = `[yandex-music]           # музыка Яндекса: openmediad играет её сам (выключено — играет штатный mediad)
enabled = true`

func init() {
	registry.Register(registry.Component{
		Name:     "yandex-music",
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
