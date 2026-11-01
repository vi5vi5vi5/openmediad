package spotifysoloist

import (
	"fmt"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/led"
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
	Buttons     bool `conf:"buttons"`      // кнопки управляют Spotify: кнопка Алисы и жесты на колонке, кнопки Bluetooth-наушников (пауза, дальше, назад)

	Music      bool   `conf:"music"`        // «Алиса, включи музыку» запускает Spotify, пока ничего не играет
	MusicURI   string `conf:"music_uri"`    // что включать, если продолжать нечего: spotify:playlist:… / album / artist; пусто — не вмешиваться
	QuietReply bool   `conf:"quiet_reply"`  // когда запрос выполняет Spotify, голос Алисы («Включаю …», «не нашла») не звучит
	PlayByName bool   `conf:"play_by_name"` // «Алиса, включи Кино / песню … / альбом …»: найти в Spotify (своё название или поиск) и запустить
	Like       bool   `conf:"like"`         // «Алиса, лайк»: поставить лайк играющему треку (нужны client_id и openmediad spotify login)
	ClientID   string `conf:"client_id"`    // Client ID приложения из Developer Dashboard Spotify (для лайков)
	WebProxy   string `conf:"web_proxy"`    // прокси для Web API Spotify (http://хост:порт, socks5://хост:порт): API закрыт для адресов некоторых стран

	VirtualPlayer bool `conf:"virtual_player"` // сообщать платформе о Spotify как о плеере музыки (кнопка, короткие фразы без «Алиса», дисплей)
}

func defaults() *Config {
	return &Config{Toggle: config.Toggle{Enabled: true}, DeviceName: "openmediad", Volume: 50, VolumeSync: true, CacheMB: 100, KeepFreeMB: 40,
		FixCP15: true, WSPort: 9090, LatencyMS: 250, Voice: true, VoiceCancel: true, Buttons: true, VirtualPlayer: true, Like: true, Music: true, PlayByName: true, QuietReply: true}
}

const template = `[spotify-soloist]        # Spotify Connect: колонка в списке устройств Spotify, играет через openmediad (опытный)
enabled = true           # работает после «openmediad spotify setup» и «openmediad spotify setkey»; до них только пишет в журнал, чего не хватает
device_name = "openmediad"  # имя колонки в списке устройств Spotify
dir = ""                 # каталог установки; пусто — /data/opt/openmediad/spotify-soloist
key_file = ""            # файл с ключом Soloist (права 0600); пусто — api.key в каталоге установки
volume_sync = true       # громкость Spotify = громкость колонки: «Алиса, тише» и кнопки двигают ползунок в приложении, ползунок меняет колонку
volume = 50              # начальная громкость Soloist, 0–100; нужна только при volume_sync = false
cache_mb = 100           # предел звукового кэша на /data, МБ (Soloist не принимает меньше 100)
keep_free_mb = 40        # сколько МБ держать свободными на /data: меньше — самые старые файлы кэша удаляются (установка новой версии openmediad просит не меньше 28 МБ)
fix_cp15 = true          # при запуске выставлять abi.cp15_barrier = 2 (до перезагрузки). ВНИМАНИЕ: это системная настройка ядра колонки;
                         # без неё Soloist зависает. Включая компонент в сборку, вы с этим соглашаетесь. false — выставляйте сами
ws_port = 9090           # порт WebSocket API Soloist (только 127.0.0.1)
latency_ms = 250         # задержка от шима до динамика, для позиции трека в приложении Spotify
verbose = false          # подробный журнал Soloist (много строк)
voice = true             # «Алиса, пауза / дальше / назад / продолжи» управляют Spotify, пока он играет (иначе облако Яндекса отвечает «не поняла»)
voice_cancel = true      # выполнив такую фразу, отменить диалог Алисы, чтобы она не отвечала поверх
buttons = true           # кнопки управляют Spotify: кнопка Алисы и жесты по колонке, кнопки Bluetooth-наушников (иначе кнопка колонки молчит, жест запускает Яндекс.Музыку, а кнопки наушников уходят платформе)
quiet_reply = true       # когда запрос («включи Кино», «включи песню …») выполняет Spotify, голос Алисы не озвучивает свой ответ; false — Алиса отвечает как обычно
play_by_name = true      # «Алиса, включи Кино», «включи песню …»: свои названия (openmediad spotify alias) и поиск в Spotify (нужны client_id, вход и из России прокси)
music = true             # «Алиса, включи музыку» запускает Spotify (продолжает прошлую сессию), если ничего не играет
music_uri = ""           # что включать, когда продолжать нечего: spotify:playlist:… (или album, artist, track); пусто — Алиса включит Яндекс
web_proxy = ""           # прокси для Web API Spotify (лайки): http://хост:порт или socks5://хост:порт; нужен, если Spotify отвечает «unavailable in this country»
client_id = ""           # Client ID вашего приложения в Developer Dashboard Spotify: нужен для лайков (см. README: openmediad spotify login)
like = true              # «Алиса, лайк» ставит лайк играющему треку в Spotify (работает после client_id и openmediad spotify login)
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
	case c.ClientID != "" && !clientIDRe.MatchString(c.ClientID):
		return fmt.Errorf("spotify-soloist.client_id: ожидаются 16–64 латинских букв и цифр из Developer Dashboard Spotify, без пробелов")
	case c.MusicURI != "" && !musicURIRe.MatchString(c.MusicURI):
		return fmt.Errorf("spotify-soloist.music_uri: ожидается spotify:playlist:…, spotify:album:…, spotify:artist:… или spotify:track:…")
	case validProxy(c.WebProxy) != nil:
		return fmt.Errorf("spotify-soloist.web_proxy: %w", validProxy(c.WebProxy))
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
			comp := newComponent(*c, eng, m, env.Logf("spotify"), ctlsock.RunDir())
			if ind, ok := registry.Use[led.Indicator](env, "led"); ok {
				comp.led = ind
			}
			if f, ok := registry.Use[func()](env, "swallow-voice"); ok {
				comp.swallow = f
			}
			return comp, nil
		},
	})
}
