package sounds

import (
	"errors"
	"strings"

	"openmediad/internal/config"
	"openmediad/internal/engine"
	"openmediad/internal/quasar/mediad"
	"openmediad/internal/registry"
)

// Config — секция [source.sounds].
type Config struct {
	config.Toggle
	Preload []string `conf:"preload"` // файлы из /system/vendor/quasar/sounds/, которые раскодировать при запуске
}

const template = `[source.sounds]          # звонки таймеров и будильников: openmediad играет их сам (выключено — играет штатный mediad)
enabled = true
preload = ["common/timer.wav"]   # раскодировать заранее (путь внутри /system/vendor/quasar/sounds/): звонок начнётся сразу`

func init() {
	registry.Register(registry.Component{
		Name: "sounds",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, Preload: []string{"common/timer.wav"}}
		},
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			eng, ok1 := registry.Use[*engine.Engine](env, "engine")
			m, ok2 := registry.Use[*mediad.Machine](env, "machine")
			if !ok1 || !ok2 {
				return nil, errors.New("sounds: нет движка или автомата mediad")
			}
			b := &Backend{Engine: eng, Events: m}
			logf := env.Logf("sound")
			for _, p := range cfg.(*Config).Preload {
				path := Dir + strings.TrimPrefix(p, "/")
				go func() { // раскодирование ≈0,5 с: не задерживает запуск
					if err := b.Preload(path); err != nil {
						logf("прогрев %s не удался: %v (звонок раскодируется при первом запуске)", path, err)
					} else {
						logf("прогрето в память: %s", path)
					}
				}()
			}
			return b, nil
		},
	})
}

// Accepts — какие файлы openmediad берёт себе: только системные звуки колонки с безопасными именами.
func (b *Backend) Accepts(path string) bool { return ValidPath(path) }
