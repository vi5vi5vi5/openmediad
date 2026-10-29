package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"openmediad/internal/core/registry"
)

// splitKey делит «секция.ключ» (секция сама может содержать точки: «yandex-music.enabled»).
func splitKey(full string) (section, key string, err error) {
	i := strings.LastIndexByte(full, '.')
	if i <= 0 || i == len(full)-1 {
		return "", "", fmt.Errorf("ключ пишется как секция.ключ, например general.log_level или yandex-music.enabled")
	}
	return full[:i], full[i+1:], nil
}

// schemaOf — структура настроек секции (для вида поля).
func schemaOf(c *Config, section string) (any, error) {
	for _, s := range c.core() {
		if s.name == section {
			return s.into, nil
		}
	}
	info, ok := registry.BySection(section)
	if !ok {
		return nil, fmt.Errorf("неизвестная секция [%s]", section)
	}
	v, ok := c.Components[info.Name]
	if !ok {
		return nil, fmt.Errorf("компонент %s не вошёл в эту сборку", info.Name)
	}
	return v, nil
}

// Get — значение ключа «секция.ключ» в действующих настройках (с умолчаниями), как оно записывается в файле.
func Get(c *Config, full string) (string, error) {
	section, key, err := splitKey(full)
	if err != nil {
		return "", err
	}
	into, err := schemaOf(c, section)
	if err != nil {
		return "", err
	}
	fields := map[string]reflect.Value{}
	collectFields(reflect.ValueOf(into).Elem(), fields)
	f, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("в [%s] нет ключа %q (есть: %s)", section, key, strings.Join(names(fields), ", "))
	}
	switch {
	case f.Type() == durationType:
		return Quote(fmt.Sprint(f.Interface())), nil
	case f.Kind() == reflect.String:
		return Quote(f.String()), nil
	case f.Kind() == reflect.Slice:
		return QuoteList(f.Interface().([]string)), nil
	}
	return fmt.Sprint(f.Interface()), nil
}

// Set меняет один ключ в тексте файла настроек, сохраняя комментарии и порядок: значение заменяется в строке ключа, а если
// ключа нет — дописывается в секцию (секция — в конец файла). Новый текст проверяется целиком; при ошибке текст не меняется.
func Set(text, full, raw string) (string, error) {
	section, key, err := splitKey(full)
	if err != nil {
		return "", err
	}
	cur := Defaults()
	into, err := schemaOf(cur, section)
	if err != nil {
		return "", err
	}
	t, ok := fieldKind(into, key)
	if !ok {
		fields := map[string]reflect.Value{}
		collectFields(reflect.ValueOf(into).Elem(), fields)
		return "", fmt.Errorf("в [%s] нет ключа %q (есть: %s)", section, key, strings.Join(names(fields), ", "))
	}
	val, err := formatValue(t, raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", full, err)
	}
	out := setLine(text, section, key, val)
	if _, err := Load(out); err != nil {
		return "", err
	}
	return out, nil
}

// setLine — текстовая правка: заменить значение ключа в секции или дописать ключ.
func setLine(text, section, key, val string) string {
	lines := strings.Split(text, "\n")
	cur := ""
	secEnd := -1 // последняя непустая строка нужной секции
	secFound := false
	for i, l := range lines {
		t := strings.TrimSpace(stripComment(l))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = strings.TrimSpace(t[1 : len(t)-1])
			if cur == section {
				secFound, secEnd = true, i
			}
			continue
		}
		if cur != section {
			continue
		}
		if t != "" {
			secEnd = i
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 || strings.TrimSpace(t[:eq]) != key {
			continue
		}
		// заменить значение, сохранив отступ перед комментарием и сам комментарий в конце строки
		code := stripComment(l)
		comment := l[len(code):]
		lines[i] = strings.TrimRight(l[:strings.IndexByte(l, '=')+1], " ") + " " + val
		if comment != "" {
			pad := len(code) - len(strings.TrimRight(code, " \t"))
			lines[i] += strings.Repeat(" ", max(pad, 1)) + comment
		}
		return strings.Join(lines, "\n")
	}
	add := key + " = " + val
	if !secFound {
		text = strings.TrimRight(text, "\n")
		if text != "" {
			text += "\n\n"
		}
		return text + "[" + section + "]\n" + add + "\n"
	}
	lines = append(lines[:secEnd+1], append([]string{add}, lines[secEnd+1:]...)...)
	return strings.Join(lines, "\n")
}

// WriteFile записывает настройки атомарно: во временный файл рядом и переименованием (обрыв питания не оставит полфайла).
func WriteFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		f.Sync()
		f.Close()
	}
	return os.Rename(tmp, path)
}

// ReadText — текст файла настроек; если файла нет — шаблон по умолчанию (и exists=false).
func ReadText(path string) (text string, exists bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Template(), false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}
