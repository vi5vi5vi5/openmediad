package meetup

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"unicode/utf8"

	"openmediad/internal/core/config"
	"openmediad/internal/core/control"
	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/quasar/alicedclient"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
)

// Config — секция [meetup]. Здесь только то, что задаётся один раз; ссылка на конференцию, имя, выключатели и громкость
// меняются на странице настроек и лежат в файле состояния (Settings).
type Config struct {
	config.Toggle
	Listen     string `conf:"listen"`     // адрес страницы настроек; пусто — без страницы
	Name       string `conf:"name"`       // имя участника по умолчанию
	Prefix     string `conf:"prefix"`     // с чего начинаются команды в чате
	Bitrate    int    `conf:"bitrate"`    // Opus, бит/с
	Complexity int    `conf:"complexity"` // сложность кодера Opus, 1–10
	WSURL      string `conf:"ws_url"`     // адрес WebSocket вручную
}

const template = `[meetup]                 # колонка как участник конференции MeetUp (опытный): звук движка в комнату, команды из чата
enabled = true           # чтобы звук шёл в конференцию, добавьте "meetup" первым в [route]: music = ["meetup", "speaker"], voice — так же
listen = "0.0.0.0:6702"  # страница настроек для компьютеров домашней сети: http://адрес-колонки:6702/ ; "" — без страницы
name = "Колонка"         # имя участника по умолчанию (меняется на странице настроек)
prefix = "!"             # с чего начинаются команды в чате: !play, !pause, !next, !алиса …
bitrate = 64000          # битрейт Opus, бит/с (моно): 16000–256000
complexity = 5           # сложность кодера Opus, 1–10: на Станции Мини 3 это 4–8 % одного ядра из четырёх
ws_url = ""              # адрес WebSocket вручную (сервер разработчика с нестандартными портами); пусто — из ссылки на конференцию`

func defaults() *Config {
	return &Config{Toggle: config.Toggle{Enabled: true}, Listen: "0.0.0.0:6702", Name: "Колонка", Prefix: "!", Bitrate: 64000, Complexity: 5}
}

func (c *Config) validate() error {
	if c.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("meetup.listen: %q — нужен адрес вида 0.0.0.0:6702 или пустая строка", c.Listen)
		}
	}
	if n := utf8.RuneCountInString(c.Name); n < 1 || n > 64 {
		return fmt.Errorf("meetup.name: имя участника — от 1 до 64 символов")
	}
	if n := utf8.RuneCountInString(c.Prefix); n > 3 {
		return fmt.Errorf("meetup.prefix: %q — не длиннее трёх символов (пусто — команды чата выключены)", c.Prefix)
	}
	if c.Bitrate < 16000 || c.Bitrate > 256000 {
		return fmt.Errorf("meetup.bitrate: %d — от 16000 до 256000 бит/с", c.Bitrate)
	}
	if c.Complexity < 1 || c.Complexity > 10 {
		return fmt.Errorf("meetup.complexity: %d — от 1 до 10", c.Complexity)
	}
	return nil
}

// statePath — файл состояния компонента (ссылка на конференцию и выключатели).
func statePath() string { return filepath.Join(layout.State(), "meetup.json") }

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
			comp := newComponent(*c, statePath(), env.Logf(Name))
			comp.tls = tlsConfig()
			comp.hub, _ = registry.Use[*control.Hub](env, "control")
			comp.mach, _ = registry.Use[*mediad.Machine](env, "machine")
			aliced := &alicedclient.Client{}
			comp.ask = func(ctx context.Context, text string) error { return aliced.Ask(ctx, text) }
			return comp, nil
		},
	})
}
