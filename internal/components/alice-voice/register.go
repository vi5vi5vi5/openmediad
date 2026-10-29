package alicevoice

import (
	"errors"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
)

// Config — секция [alice-voice].
type Config struct {
	config.Toggle
}

const template = `[alice-voice]            # голос Алисы: openmediad декодирует и играет ответы сам (выключено — говорит штатный mediad)
enabled = true`

func init() {
	registry.Register(registry.Component{
		Name:     "alice-voice",
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
