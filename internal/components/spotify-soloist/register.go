package spotifysoloist

import (
	"fmt"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
)

// Name — имя компонента: папка, секция настроек, имя в -with и -without.
const Name = "spotify-soloist"

// Config — секция [spotify-soloist].
type Config struct {
	config.Toggle
	DeviceName string `conf:"device_name"`  // имя колонки в списке устройств Spotify
	Dir        string `conf:"dir"`          // каталог установки; пусто — /data/opt/openmediad/spotify-soloist
	KeyFile    string `conf:"key_file"`     // файл с ключом Soloist; пусто — <dir>/api.key
	Volume     int    `conf:"volume"`       // начальная громкость Soloist, 0–100 (если volume_sync = false)
	VolumeSync bool   `conf:"volume_sync"`  // громкость Spotify = громкость колонки (регулятор Master)
	CacheMB    int    `conf:"cache_mb"`     // предел звукового кэша, МБ (Soloist: не меньше 100)
	KeepFreeMB int    `conf:"keep_free_mb"` // сколько МБ держать свободными на /data
	FixCP15    bool   `conf:"fix_cp15"`     // при запуске выставить abi.cp15_barrier = 2
	WSPort     int    `conf:"ws_port"`      // порт WebSocket API Soloist на 127.0.0.1
	LatencyMS  int    `conf:"latency_ms"`   // задержка от шима до динамика (для позиции в приложении Spotify)
	Verbose    bool   `conf:"verbose"`      // подробный журнал Soloist (много строк)

	Voice       bool `conf:"voice"`        // «Алиса, пауза / дальше / назад / продолжи» управляют Spotify, пока он играет
	VoiceCancel bool `conf:"voice_cancel"` // выполнив такую фразу, отменить диалог Алисы, чтобы облако не отвечало «не поняла»

	VirtualPlayer bool `conf:"virtual_player"` // сообщать платформе о Spotify как о плеере музыки (кнопка, короткие фразы без «Алиса», дисплей)
}

func defaults() *Config {
	return &Config{Toggle: config.Toggle{Enabled: true}, DeviceName: "openmediad", Volume: 50, VolumeSync: true, CacheMB: 100, KeepFreeMB: 25,
		FixCP15: true, WSPort: 9090, LatencyMS: 250, Voice: true, VoiceCancel: true, VirtualPlayer: true}
}

const template = `[spotify-soloist]        # Spotify Connect: колонка в списке устройств Spotify, играет через openmediad (опытный)
enabled = true           # работает после «openmediad spotify setup» и «openmediad spotify setkey»; до них только пишет в журнал, чего не хватает
device_name = "openmediad"  # имя колонки в списке устройств Spotify
dir = ""                 # каталог установки; пусто — /data/opt/openmediad/spotify-soloist
key_file = ""            # файл с ключом Soloist (права 0600); пусто — api.key в каталоге установки
volume_sync = true       # громкость Spotify = громкость колонки: «Алиса, тише» и кнопки двигают ползунок в приложении, ползунок меняет колонку
volume = 50              # начальная громкость Soloist, 0–100; нужна только при volume_sync = false
cache_mb = 100           # предел звукового кэша на /data, МБ (Soloist не принимает меньше 100)
keep_free_mb = 25        # сколько МБ держать свободными на /data: меньше — самые старые файлы кэша удаляются
fix_cp15 = true          # при запуске выставлять abi.cp15_barrier = 2 (до перезагрузки). ВНИМАНИЕ: это системная настройка ядра колонки;
                         # без неё Soloist зависает. Включая компонент в сборку, вы с этим соглашаетесь. false — выставляйте сами
ws_port = 9090           # порт WebSocket API Soloist (только 127.0.0.1)
latency_ms = 250         # задержка от шима до динамика, для позиции трека в приложении Spotify
verbose = false          # подробный журнал Soloist (много строк)
voice = true             # «Алиса, пауза / дальше / назад / продолжи» управляют Spotify, пока он играет (иначе облако Яндекса отвечает «не поняла»)
voice_cancel = true      # выполнив такую фразу, отменить диалог Алисы, чтобы она не отвечала поверх
virtual_player = true    # сообщать платформе о Spotify как о плеере музыки: тогда платформа знает, что играет (опытное; false — Spotify для неё невидим)`

func (c *Config) validate() error {
	switch {
	case strings.TrimSpace(c.DeviceName) == "" || len(c.DeviceName) > 64 || strings.ContainsAny(c.DeviceName, "\r\n\x00"):
		return fmt.Errorf("spotify-soloist.device_name: нужно имя до 64 символов без переводов строки")
	case c.Volume < 0 || c.Volume > 100:
		return fmt.Errorf("spotify-soloist.volume: %d — от 0 до 100", c.Volume)
	case c.CacheMB < 100 || c.CacheMB > 2000:
		return fmt.Errorf("spotify-soloist.cache_mb: %d — Soloist принимает от 100 МБ; на колонке /data около 150 МБ, берите 100", c.CacheMB)
	case c.KeepFreeMB < 5 || c.KeepFreeMB > 500:
		return fmt.Errorf("spotify-soloist.keep_free_mb: %d — от 5 до 500", c.KeepFreeMB)
	case c.WSPort < 1024 || c.WSPort > 65535:
		return fmt.Errorf("spotify-soloist.ws_port: %d — от 1024 до 65535", c.WSPort)
	case c.LatencyMS < 0 || c.LatencyMS > 2000:
		return fmt.Errorf("spotify-soloist.latency_ms: %d — от 0 до 2000", c.LatencyMS)
	}
	return nil
}

func init() {
	registry.Register(registry.Component{
		Name:     Name,
		Defaults: func() any { return defaults() },
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			c := cfg.(*Config)
			if err := c.validate(); err != nil {
				return nil, err
			}
			eng, ok := registry.Use[*engine.Engine](env, "engine")
			if !ok {
				return nil, fmt.Errorf("spotify-soloist: нет движка")
			}
			var m machine
			if mm, ok := registry.Use[*mediad.Machine](env, "machine"); ok && mm != nil {
				m = mm
			}
			return newComponent(*c, eng, m, env.Logf("spotify"), ctlsock.RunDir()), nil
		},
	})
}
