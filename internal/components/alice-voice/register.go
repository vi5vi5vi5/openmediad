package alicevoice

import (
	"errors"
	"fmt"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/alsactl"
)

// Config — секция [alice-voice].
type Config struct {
	config.Toggle
	AnswerVolume string `conf:"answer_volume"` // platform | hold
}

func (c *Config) validate() error {
	if c.AnswerVolume != AnswerVolumePlatform && c.AnswerVolume != AnswerVolumeHold {
		return fmt.Errorf("alice-voice.answer_volume: %q — допустимо platform или hold", c.AnswerVolume)
	}
	return nil
}

// masterControl — регулятор громкости колонки (элемент ALSA; amixer называет его «Master»).
const masterControl = "Master Playback Volume"

// openMaster открывает регулятор колонки (подменяется в тестах).
var openMaster = func() (masterCtl, error) { return alsactl.WatchInteger(masterControl) }

const template = `[alice-voice]            # голос Алисы: openmediad декодирует и играет ответы сам (выключено — говорит штатный mediad)
enabled = true
answer_volume = "platform"  # громкость ответов: platform — как решит колонка (она сама делает ответ громче или тише по шуму в комнате,
                         # поэтому соседние ответы могут звучать по-разному); hold — держать громкость, которая была до ответа
                         # (опытное; только динамики: громкость Bluetooth-наушников колонка ставит им сама)`

func init() {
	registry.Register(registry.Component{
		Name: "alice-voice",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, AnswerVolume: AnswerVolumePlatform}
		},
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			eng, ok1 := registry.Use[*engine.Engine](env, "engine")
			m, ok2 := registry.Use[*mediad.Machine](env, "machine")
			if !ok1 || !ok2 {
				return nil, errors.New("tts: нет движка или автомата mediad")
			}
			c := cfg.(*Config)
			if err := c.validate(); err != nil {
				return nil, err
			}
			b := &Backend{Engine: eng, Events: m, Logf: env.Logf("voice")}
			b.setAnswerVolume(c.AnswerVolume)
			return b, nil
		},
	})
}
