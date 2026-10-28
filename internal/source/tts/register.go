package tts

import (
	"errors"

	"openmediad/internal/config"
	"openmediad/internal/engine"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/registry"
)

// Config — секция [source.tts].
type Config struct {
	config.Toggle
}

const template = `[source.tts]             # голос Алисы: openmediad декодирует и играет ответы сам (выключено — говорит штатный mediad)
enabled = true`

func init() {
	registry.Register(registry.Component{
		Name:     "tts",
		Defaults: func() any { return &Config{Toggle: config.Toggle{Enabled: true}} },
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			eng, ok1 := registry.Use[*engine.Engine](env, "engine")
			m, ok2 := registry.Use[*mediad.Machine](env, "machine")
			if !ok1 || !ok2 {
				return nil, errors.New("tts: нет движка или автомата mediad")
			}
			return &Backend{Engine: eng, Events: m, Logf: env.Logf("voice")}, nil
		},
	})
}
