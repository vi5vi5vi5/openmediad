package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/registry"
)

// DefaultPath — где лежат настройки на колонке. Другой путь — переменной окружения OPENMEDIAD_CONFIG.
const DefaultPath = "/data/opt/openmediad/openmediad.conf"

// Path — путь к файлу настроек с учётом OPENMEDIAD_CONFIG (и OPENMEDIAD_ROOT симулятора колонки, см. system/layout).
func Path() string {
	if p := os.Getenv("OPENMEDIAD_CONFIG"); p != "" {
		return p
	}
	return os.Getenv("OPENMEDIAD_ROOT") + DefaultPath
}

// Config — все настройки. Секции компонентов — в Components (по имени компонента, значение — структура его настроек).
type Config struct {
	General  General
	Fallback Fallback
	Route    Route
	Focus    Focus
	Speaker  Speaker

	Components map[string]any
	Warnings   []string // некритичные замечания (секции компонентов, которых нет в сборке, и т. п.)
}

// General — [general].
type General struct {
	Mode         string `conf:"mode"`            // shim | replace
	LogLevel     string `conf:"log_level"`       // trace | info | warn | error
	LogEntries   int    `conf:"log_entries"`     // записей журнала в памяти
	LogFile      string `conf:"log_file"`        // файл журнала (пусто — только в памяти)
	LogFileMaxKB int    `conf:"log_file_max_kb"` // предел файла журнала
}

// Fallback — [fallback]: возврат к штатному mediad. В режиме shim штатный mediad работает всегда и подхватывает всё,
// что openmediad не смог, поэтому выключателя нет; он появится с режимом replace (фаза 12: холодный резерв).
type Fallback struct {
	MaxCrashes int `conf:"max_crashes"`
}

// Route — [route]: выходы для классов потоков по приоритету.
type Route struct {
	Music []string `conf:"music"`
	Voice []string `conf:"voice"`
	Alert []string `conf:"alert"`
}

// Focus — [focus]: приглушение музыки во время диалога.
type Focus struct {
	DuckGain float64 `conf:"duck_gain"`
}

// Speaker — [speaker]: динамики колонки.
type Speaker struct {
	Device string `conf:"device"`
	Aplay  string `conf:"aplay"`
}

// core — секции ядра и куда они раскладываются.
func (c *Config) core() []struct {
	name string
	into any
} {
	return []struct {
		name string
		into any
	}{
		{"general", &c.General}, {"fallback", &c.Fallback}, {"route", &c.Route}, {"focus", &c.Focus}, {"speaker", &c.Speaker},
	}
}

// Defaults — настройки по умолчанию (как без файла).
func Defaults() *Config {
	c := &Config{
		General:    General{Mode: "shim", LogLevel: journal.Info, LogEntries: 2000, LogFileMaxKB: 1024},
		Fallback:   Fallback{MaxCrashes: 3},
		Route:      Route{Music: []string{"speaker"}, Voice: []string{"speaker"}, Alert: []string{"speaker"}},
		Focus:      Focus{DuckGain: 0.25},
		Speaker:    Speaker{Device: "default", Aplay: "/usr/bin/aplay"},
		Components: map[string]any{},
	}
	for _, rc := range registry.All() {
		if rc.Defaults != nil {
			c.Components[rc.Name] = rc.Defaults()
		}
	}
	return c
}

// Load разбирает и проверяет текст настроек.
func Load(text string) (*Config, error) {
	doc, err := Parse(text)
	if err != nil {
		return nil, err
	}
	c := Defaults()
	core := map[string]any{}
	for _, s := range c.core() {
		core[s.name] = s.into
	}
	for _, sec := range doc.Sections {
		if sec.Name == "" {
			if len(sec.Keys) > 0 {
				return nil, &ParseError{sec.Keys[0].Value.Line, "ключи до первой секции: сначала укажите секцию, например [general]"}
			}
			continue
		}
		if into, ok := core[sec.Name]; ok {
			if err := Decode(sec, into); err != nil {
				return nil, err
			}
			continue
		}
		info, known := registry.BySection(sec.Name)
		if !known {
			return nil, &ParseError{sec.Line, fmt.Sprintf("неизвестная секция [%s]", sec.Name)}
		}
		into, compiled := c.Components[info.Name]
		if !compiled {
			c.Warnings = append(c.Warnings, fmt.Sprintf("строка %d: секция [%s] — компонент %s не вошёл в эту сборку, секция пропущена", sec.Line, sec.Name, info.Name))
			continue
		}
		if err := Decode(sec, into); err != nil {
			return nil, err
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadFile читает настройки из файла; нет файла — умолчания.
func LoadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return nil, err
	}
	c, err := Load(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Sinks — имена выходов, которые есть в этой сборке (динамики всегда).
func Sinks() []string {
	out := []string{"speaker"}
	for _, c := range registry.Catalog {
		if c.Kind == registry.Sink {
			if _, ok := registry.Get(c.Name); ok {
				out = append(out, c.Name)
			}
		}
	}
	return out
}

func (c *Config) validate() error {
	g := c.General
	if g.Mode != "shim" && g.Mode != "replace" {
		return fmt.Errorf("[general] mode: %q — допустимо \"shim\" или \"replace\"", g.Mode)
	}
	if !journal.ValidLevel(g.LogLevel) {
		return fmt.Errorf("[general] log_level: %q — допустимо trace, info, warn, error", g.LogLevel)
	}
	if g.LogEntries < 100 || g.LogEntries > 100000 {
		return fmt.Errorf("[general] log_entries: %d — допустимо от 100 до 100000", g.LogEntries)
	}
	if g.LogFileMaxKB < 16 {
		return fmt.Errorf("[general] log_file_max_kb: %d — не меньше 16", g.LogFileMaxKB)
	}
	if g.LogFile != "" && !strings.HasPrefix(g.LogFile, "/") {
		return fmt.Errorf("[general] log_file: нужен полный путь, например \"/data/opt/openmediad/log/openmediad.log\"")
	}
	if c.Fallback.MaxCrashes < 1 {
		return fmt.Errorf("[fallback] max_crashes: %d — не меньше 1", c.Fallback.MaxCrashes)
	}
	if d := c.Focus.DuckGain; d < 0 || d > 1 {
		return fmt.Errorf("[focus] duck_gain: %v — от 0 (тишина) до 1 (без приглушения)", d)
	}
	sinks := Sinks()
	for _, r := range []struct {
		name string
		list *[]string
	}{{"music", &c.Route.Music}, {"voice", &c.Route.Voice}, {"alert", &c.Route.Alert}} {
		if len(*r.list) == 0 {
			return fmt.Errorf("[route] %s: нужен хотя бы один выход, например [\"speaker\"]", r.name)
		}
		var kept []string
		for _, s := range *r.list {
			switch {
			case slices.Contains(sinks, s):
				kept = append(kept, s)
			case isCatalogSink(s):
				c.Warnings = append(c.Warnings, fmt.Sprintf("[route] %s: выход %q не вошёл в эту сборку, пропущен", r.name, s))
			default:
				return fmt.Errorf("[route] %s: неизвестный выход %q (есть: %s)", r.name, s, strings.Join(sinks, ", "))
			}
		}
		if len(kept) == 0 {
			kept = []string{"speaker"}
			c.Warnings = append(c.Warnings, fmt.Sprintf("[route] %s: ни один выход не доступен в этой сборке — звук пойдёт на динамики", r.name))
		}
		*r.list = kept
	}
	return nil
}

func isCatalogSink(name string) bool {
	i, ok := registry.Lookup(name)
	return ok && i.Kind == registry.Sink
}

// Enabled — включён ли компонент сборки в настройках (у компонента без настроек — всегда).
func (c *Config) Enabled(name string) bool {
	if _, ok := registry.Get(name); !ok {
		return false
	}
	v, ok := c.Components[name]
	if !ok {
		return true
	}
	t, ok := v.(interface{ On() bool })
	return !ok || t.On()
}
