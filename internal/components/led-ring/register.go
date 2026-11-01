package ledring

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"openmediad/internal/core/config"
	"openmediad/internal/core/led"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/ringled"
)

// Name — имя компонента: папка, секция настроек, имя в -with и -without.
const Name = "led-ring"

// Config — секция [led-ring].
type Config struct {
	config.Toggle
	SourceZone       int           `conf:"source_zone"`       // зона кольца (0…5), где горит источник музыки
	OutputZone       int           `conf:"output_zone"`       // зона кольца (0…5), где горит выход звука
	SourceBrightness int           `conf:"source_brightness"` // яркость источника, 0…255
	OutputBrightness int           `conf:"output_brightness"` // яркость выхода, 0…255
	Hold             time.Duration `conf:"hold"`              // сколько кольцо горит на полную, когда началась музыка; 0 — пока играет
	Fade             time.Duration `conf:"fade"`              // за сколько оно потом плавно гаснет
	Colors           string        `conf:"colors"`            // цвета по именам модулей: "yandex-music=#ffb000 spotify-soloist=#00ff40 bt-out=#0060ff"
}

func defaults() *Config {
	return &Config{Toggle: config.Toggle{Enabled: true}, SourceZone: 3, OutputZone: 0, SourceBrightness: 60, OutputBrightness: 60, Hold: 5 * time.Second, Fade: 2 * time.Second}
}

const template = `[led-ring]               # подсветка логотипа Мини 3: какой источник играет и куда идёт звук (шесть зон кольца, /dev/fb0)
enabled = true
source_zone = 3          # зона 0…5, где горит источник музыки (Яндекс, Spotify …); зоны считаются по кольцу
output_zone = 0          # зона 0…5, где горит выход звука (наушники …); нет цвета у выхода — зона не горит
source_brightness = 60   # яркость источника, 0…255
output_brightness = 60   # яркость выхода, 0…255
hold = "5s"              # сколько кольцо горит на полную, когда началась музыка (вместе со спектрограммой); "0s" — пока музыка играет
fade = "2s"              # за сколько оно потом плавно гаснет; "0s" — гаснет сразу
colors = ""              # свои цвета по именам модулей через пробел, #rrggbb: "yandex-music=#ffb000 spotify-soloist=#00ff40 bt-out=#0060ff speaker=#ffffff";
                         # у модулей есть свои цвета (Яндекс жёлтый, Spotify зелёный, наушники синие), здесь их можно поменять`

func init() {
	registry.Register(registry.Component{
		Name:     Name,
		Defaults: func() any { return defaults() },
		Template: template,
		New:      newComponent,
	})
}

// zonesOK и остальные проверки: настройки проверяются при загрузке файла и при перезагрузке на ходу.
func (c *Config) validate() error {
	for _, z := range []struct {
		key string
		v   int
	}{{"source_zone", c.SourceZone}, {"output_zone", c.OutputZone}} {
		if z.v < 0 || z.v >= ringled.Zones {
			return fmt.Errorf("led-ring.%s: %d — зоны кольца 0…%d", z.key, z.v, ringled.Zones-1)
		}
	}
	for _, b := range []struct {
		key string
		v   int
	}{{"source_brightness", c.SourceBrightness}, {"output_brightness", c.OutputBrightness}} {
		if b.v < 0 || b.v > 255 {
			return fmt.Errorf("led-ring.%s: %d — яркость 0…255", b.key, b.v)
		}
	}
	if c.Hold < 0 || c.Hold > 10*time.Minute {
		return fmt.Errorf("led-ring.hold: %v — от 0s (пока играет) до 10m", c.Hold)
	}
	if c.Fade < 0 || c.Fade > 30*time.Second {
		return fmt.Errorf("led-ring.fade: %v — от 0s до 30s", c.Fade)
	}
	if _, err := parseColors(c.Colors); err != nil {
		return fmt.Errorf("led-ring.colors: %w", err)
	}
	return nil
}

// parseColors разбирает «имя=#rrggbb имя=#rrggbb» (разделители — пробелы и запятые).
func parseColors(s string) (map[string]led.Color, error) {
	out := map[string]led.Color{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == ';' }) {
		name, hex, ok := strings.Cut(f, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q: нужно имя=#rrggbb", f)
		}
		c, err := led.ParseColor(hex)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = c
	}
	return out, nil
}

// formatColors — цвета обратно в строку настроек (для статуса), по алфавиту.
func formatColors(m map[string]led.Color) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k].Hex())
	}
	return strings.Join(parts, " ")
}
