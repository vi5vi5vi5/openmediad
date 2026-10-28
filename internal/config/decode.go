package config

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Toggle — ключ enabled: его встраивают настройки компонентов, которые можно выключить без пересборки.
type Toggle struct {
	Enabled bool `conf:"enabled"`
}

// On — включён ли компонент.
func (t Toggle) On() bool { return t.Enabled }

var durationType = reflect.TypeOf(time.Duration(0))

// Decode раскладывает секцию в структуру по тегам `conf:"ключ"` (into — указатель на структуру с умолчаниями; ключей, которых
// нет в файле, Decode не трогает). Встроенные структуры (Toggle) раскрываются. Неизвестный ключ — ошибка.
func Decode(sec *Section, into any) error {
	fields := map[string]reflect.Value{}
	collectFields(reflect.ValueOf(into).Elem(), fields)
	for _, k := range sec.Keys {
		f, ok := fields[k.Name]
		if !ok {
			return &ParseError{k.Value.Line, fmt.Sprintf("[%s]: неизвестный ключ %q (есть: %s)", sec.Name, k.Name, strings.Join(names(fields), ", "))}
		}
		if err := assign(f, k.Value); err != nil {
			return &ParseError{k.Value.Line, fmt.Sprintf("[%s] %s: %v", sec.Name, k.Name, err)}
		}
	}
	return nil
}

func collectFields(v reflect.Value, out map[string]reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			collectFields(v.Field(i), out)
			continue
		}
		if tag := f.Tag.Get("conf"); tag != "" && tag != "-" {
			out[tag] = v.Field(i)
		}
	}
}

func names(m map[string]reflect.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assign(f reflect.Value, v Value) error {
	switch {
	case f.Type() == durationType:
		if v.Kind != KindString {
			return fmt.Errorf("нужна длительность строкой, например \"300ms\" или \"2s\"")
		}
		d, err := time.ParseDuration(v.Str)
		if err != nil {
			return fmt.Errorf("непонятная длительность %q (примеры: \"300ms\", \"2s\", \"1m\")", v.Str)
		}
		f.SetInt(int64(d))
	case f.Kind() == reflect.String:
		if v.Kind != KindString {
			return fmt.Errorf("нужна строка в кавычках, а записано: %s", v.Kind)
		}
		f.SetString(v.Str)
	case f.Kind() == reflect.Bool:
		if v.Kind != KindBool {
			return fmt.Errorf("нужно true или false, а записано: %s", v.Kind)
		}
		f.SetBool(v.Bool)
	case f.Kind() == reflect.Int:
		if v.Kind != KindNumber || v.Num != math.Trunc(v.Num) {
			return fmt.Errorf("нужно целое число")
		}
		f.SetInt(int64(v.Num))
	case f.Kind() == reflect.Float64:
		if v.Kind != KindNumber {
			return fmt.Errorf("нужно число")
		}
		f.SetFloat(v.Num)
	case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
		if v.Kind != KindList {
			return fmt.Errorf("нужен список строк, например [\"a\", \"b\"]")
		}
		f.Set(reflect.ValueOf(append([]string(nil), v.List...)))
	default:
		return fmt.Errorf("поле настроек неподдерживаемого вида %s", f.Type())
	}
	return nil
}

// fieldKind — вид поля структуры настроек по имени ключа (для config set).
func fieldKind(into any, key string) (reflect.Type, bool) {
	fields := map[string]reflect.Value{}
	collectFields(reflect.ValueOf(into).Elem(), fields)
	f, ok := fields[key]
	if !ok {
		return nil, false
	}
	return f.Type(), true
}

// formatValue превращает значение из командной строки в запись для файла по виду поля: строки — в кавычки, списки — из
// «a,b» или ["a","b"], числа и да/нет — как есть (с проверкой).
func formatValue(t reflect.Type, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case t == durationType:
		s := strings.Trim(raw, `"`)
		if _, err := time.ParseDuration(s); err != nil {
			return "", fmt.Errorf("непонятная длительность %q (примеры: 300ms, 2s, 1m)", s)
		}
		return Quote(s), nil
	case t.Kind() == reflect.String:
		if strings.HasPrefix(raw, `"`) {
			if v, err := parseValue(raw); err == nil && v.Kind == KindString {
				return raw, nil
			}
		}
		return Quote(raw), nil
	case t.Kind() == reflect.Bool:
		switch raw {
		case "true", "да", "on", "1":
			return "true", nil
		case "false", "нет", "off", "0":
			return "false", nil
		}
		return "", fmt.Errorf("нужно true или false")
	case t.Kind() == reflect.Int:
		if _, err := strconv.Atoi(raw); err != nil {
			return "", fmt.Errorf("нужно целое число")
		}
		return raw, nil
	case t.Kind() == reflect.Float64:
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			return "", fmt.Errorf("нужно число")
		}
		return raw, nil
	case t.Kind() == reflect.Slice:
		if strings.HasPrefix(raw, "[") {
			v, err := parseList(raw)
			if err != nil {
				return "", err
			}
			return QuoteList(v.List), nil
		}
		var l []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				l = append(l, p)
			}
		}
		return QuoteList(l), nil
	}
	return "", fmt.Errorf("поле неподдерживаемого вида %s", t)
}
