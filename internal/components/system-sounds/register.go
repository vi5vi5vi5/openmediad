package systemsounds

import (
	"errors"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
)

// Config — секция [system-sounds].
type Config struct {
	config.Toggle
	Preload []string `conf:"preload"` // файлы из /system/vendor/quasar/sounds/, которые раскодировать при запуске
}

// DefaultPreload — что раскодировать при запуске: звонок таймера и одноразовые звуки (после «Алиса», отмена, щелчок громкости,
// включение и выключение Bluetooth). Без прогрева первый такой звук ждёт запуска GStreamer: на колонке 28.10.2026 сигнал
// Bluetooth зазвучал через 0,55 с вместо ≈0,15 с у mediad. Всё вместе в памяти — ≈2,5 МБ (больше всего занимает звонок).
var DefaultPreload = []string{"common/timer.wav", "common/spotter_start.mp3", "common/spotter_cancel.mp3", "common/volume_sound.mp3",
	"common/bluetooth_turning_on_jingle.opus", "common/bluetooth_turning_off_jingle.opus"}

const template = `[system-sounds]          # звонки таймеров и будильников, системные звуки: openmediad играет их сам (выключено — играет штатный mediad)
enabled = true
# раскодировать заранее (пути внутри /system/vendor/quasar/sounds/): звук начнётся сразу, без запуска GStreamer
preload = ["common/timer.wav", "common/spotter_start.mp3", "common/spotter_cancel.mp3", "common/volume_sound.mp3", "common/bluetooth_turning_on_jingle.opus", "common/bluetooth_turning_off_jingle.opus"]`

func init() {
	registry.Register(registry.Component{
		Name: "system-sounds",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, Preload: append([]string(nil), DefaultPreload...)}
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
			// раскодирование ≈0,5 с на файл: в фоне, чтобы не задерживать запуск, и по очереди — GStreamer на время разбора
			// занимает ≈30 МБ, несколько сразу колонке ни к чему
			preload := cfg.(*Config).Preload
			go func() {
				for _, p := range preload {
					path := Dir + strings.TrimPrefix(p, "/")
					if err := b.Preload(path); err != nil {
						logf("прогрев %s не удался: %v (звук раскодируется при первом запуске)", path, err)
					} else {
						logf("прогрето в память: %s", path)
					}
				}
			}()
			return b, nil
		},
	})
}

// Accepts — какие файлы openmediad берёт себе: только системные звуки колонки с безопасными именами.
func (b *Backend) Accepts(path string) bool { return ValidPath(path) }
