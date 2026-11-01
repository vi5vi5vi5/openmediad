package config

import (
	"fmt"
	"reflect"
	"strings"

	"openmediad/internal/core/registry"
)

// Added — ключ, которого не было в файле и который дописал Upgrade.
type Added struct{ Section, Key string }

func (a Added) String() string { return a.Section + "." + a.Key }

// Upgrade дописывает в текст настроек всё, чего в нём не хватает: ключи, которые можно настроить в этой сборке (ядро и её компоненты), и целые секции.
// Новые строки берутся из шаблона по умолчанию вместе с комментариями; то, что уже есть в файле (значения, порядок, комментарии), не меняется.
// Нужна после обновления движка: новые версии получают новые ключи, а файл, правленный руками, о них не знает. Текст должен проходить проверку.
func Upgrade(text string) (string, []Added, error) {
	if _, err := Load(text); err != nil {
		return "", nil, fmt.Errorf("сначала исправьте файл: %w", err)
	}
	doc, err := Parse(text)
	if err != nil {
		return "", nil, err
	}
	have := map[string]map[string]bool{}
	for _, s := range doc.Sections {
		if s.Name == "" {
			continue
		}
		if have[s.Name] == nil {
			have[s.Name] = map[string]bool{}
		}
		for _, k := range s.Keys {
			have[s.Name][k.Name] = true
		}
	}

	def := Defaults()
	tpl := parseTemplate(Template())
	var added []Added
	out := text
	for _, sec := range schemaSections(def) {
		var missing []string
		for _, key := range sec.keys {
			if !have[sec.name][key] {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			continue
		}
		var lines []string
		for _, key := range missing {
			lines = append(lines, tpl.keyLines(sec.name, key, func() string {
				v, _ := Get(def, sec.name+"."+key)
				return key + " = " + v
			})...)
			added = append(added, Added{sec.name, key})
		}
		if _, present := have[sec.name]; present {
			out = insertIntoSection(out, sec.name, lines)
		} else {
			header := tpl.header(sec.name)
			out = strings.TrimRight(out, "\n") + "\n\n" + header + "\n" + strings.Join(lines, "\n") + "\n"
		}
	}
	if len(added) == 0 {
		return text, nil, nil
	}
	if _, err := Load(out); err != nil {
		return "", nil, fmt.Errorf("после дописывания файл не проходит проверку (это ошибка движка): %w", err)
	}
	return out, added, nil
}

type schemaSection struct {
	name string
	keys []string // в порядке полей структуры
}

// schemaSections — секции ядра и компонентов этой сборки и их ключи.
func schemaSections(c *Config) []schemaSection {
	var out []schemaSection
	for _, s := range c.core() {
		out = append(out, schemaSection{s.name, orderedKeys(s.into)})
	}
	for _, rc := range registry.All() {
		info, ok := registry.Lookup(rc.Name)
		v, has := c.Components[rc.Name]
		if !ok || info.Section == "" || !has {
			continue
		}
		out = append(out, schemaSection{info.Section, orderedKeys(v)})
	}
	return out
}

// orderedKeys — ключи структуры настроек в порядке объявления полей (встроенный Toggle даёт enabled первым).
func orderedKeys(into any) []string {
	var keys []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			if tag := f.Tag.Get("conf"); tag != "" && tag != "-" {
				keys = append(keys, tag)
			}
		}
	}
	walk(reflect.TypeOf(into).Elem())
	return keys
}

// tplDoc — шаблон по умолчанию, разобранный на секции: строки ключей вместе с продолжением комментария на следующих строках.
type tplDoc struct {
	headers map[string]string
	keys    map[string]map[string][]string
}

func parseTemplate(text string) tplDoc {
	d := tplDoc{headers: map[string]string{}, keys: map[string]map[string][]string{}}
	lines := strings.Split(text, "\n")
	cur := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(stripComment(l))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = strings.TrimSpace(t[1 : len(t)-1])
			d.headers[cur] = strings.TrimRight(l, " \t")
			d.keys[cur] = map[string][]string{}
			continue
		}
		eq := strings.IndexByte(t, '=')
		if cur == "" || eq < 0 {
			continue
		}
		key := strings.TrimSpace(t[:eq])
		block := []string{strings.TrimRight(l, " \t")}
		for i+1 < len(lines) && isContinuation(lines[i+1]) { // комментарий, продолжающий пояснение к ключу
			i++
			block = append(block, strings.TrimRight(lines[i], " \t"))
		}
		if _, dup := d.keys[cur][key]; !dup {
			d.keys[cur][key] = block
		}
	}
	return d
}

// isContinuation — строка только с комментарием и с отступом: так в шаблоне продолжается пояснение к ключу.
func isContinuation(l string) bool {
	return len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && strings.HasPrefix(strings.TrimSpace(l), "#")
}

func (d tplDoc) keyLines(section, key string, fallback func() string) []string {
	if b, ok := d.keys[section][key]; ok {
		return b
	}
	return []string{fallback()}
}

func (d tplDoc) header(section string) string {
	if h, ok := d.headers[section]; ok {
		return h
	}
	return "[" + section + "]"
}

// insertIntoSection вставляет строки в конец секции (после последней непустой строки, до пустых строк перед следующей секцией).
func insertIntoSection(text, section string, add []string) string {
	lines := strings.Split(text, "\n")
	cur, end := "", -1
	for i, l := range lines {
		t := strings.TrimSpace(stripComment(l))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = strings.TrimSpace(t[1 : len(t)-1])
			if cur == section {
				end = i
			}
			continue
		}
		if cur == section && strings.TrimSpace(l) != "" {
			end = i
		}
	}
	if end < 0 {
		return text
	}
	return strings.Join(append(lines[:end+1], append(append([]string(nil), add...), lines[end+1:]...)...), "\n")
}
