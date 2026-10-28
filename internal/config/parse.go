// Package config — файл настроек openmediad: разбор, проверка, умолчания, шаблон с комментариями и правка отдельных ключей.
//
// Формат — подмножество TOML, чтобы файл было удобно править в nano или vi:
//
//	# комментарий
//	[source.yandex]          секция (имя из букв, цифр, «_», «-» и точек)
//	enabled = true           ключ = значение
//	listen = "0.0.0.0:6701"  строка в кавычках (экранирование \" \\ \n \t)
//	music = ["bluetooth", "speaker"]   список строк в одну строку
//	duck_gain = 0.25         число
//	delay = "300ms"          длительность — строкой
//
// Парсер свой (без зависимостей), ошибки — с номером строки. Синтаксис совместим с TOML: при желании парсер заменяется
// библиотекой без правки файлов.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind — вид значения.
type Kind int

const (
	KindString Kind = iota
	KindBool
	KindNumber
	KindList
)

func (k Kind) String() string {
	return [...]string{"строка", "да/нет", "число", "список"}[k]
}

// Value — значение ключа.
type Value struct {
	Kind Kind
	Str  string   // KindString
	Bool bool     // KindBool
	Num  float64  // KindNumber
	Raw  string   // KindNumber: как записано (чтобы отличать целые)
	List []string // KindList
	Line int
}

// Doc — разобранный файл: секции по порядку, в секции ключи по порядку.
type Doc struct {
	Sections []*Section
}

// Section — одна секция. Ключи до первой секции попадают в секцию с пустым именем.
type Section struct {
	Name string
	Line int
	Keys []Key
}

// Key — ключ со значением.
type Key struct {
	Name  string
	Value Value
}

// Section возвращает секцию по имени (nil — нет).
func (d *Doc) Section(name string) *Section {
	for _, s := range d.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Get — значение ключа секции.
func (s *Section) Get(key string) (Value, bool) {
	for _, k := range s.Keys {
		if k.Name == key {
			return k.Value, true
		}
	}
	return Value{}, false
}

// ParseError — ошибка разбора с номером строки.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("строка %d: %s", e.Line, e.Msg) }

// Parse разбирает текст файла настроек.
func Parse(text string) (*Doc, error) {
	d := &Doc{}
	cur := &Section{}
	d.Sections = append(d.Sections, cur)
	seen := map[string]bool{"": true}
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n := i + 1
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, &ParseError{n, "секция должна заканчиваться «]»"}
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if !validName(name, true) {
				return nil, &ParseError{n, fmt.Sprintf("недопустимое имя секции %q", name)}
			}
			if seen[name] {
				return nil, &ParseError{n, fmt.Sprintf("секция [%s] повторяется", name)}
			}
			seen[name] = true
			cur = &Section{Name: name, Line: n}
			d.Sections = append(d.Sections, cur)
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, &ParseError{n, "ждали «ключ = значение»"}
		}
		key := strings.TrimSpace(line[:eq])
		if !validName(key, false) {
			return nil, &ParseError{n, fmt.Sprintf("недопустимое имя ключа %q", key)}
		}
		if _, dup := cur.Get(key); dup {
			return nil, &ParseError{n, fmt.Sprintf("ключ %q повторяется", key)}
		}
		v, err := parseValue(strings.TrimSpace(line[eq+1:]))
		if err != nil {
			return nil, &ParseError{n, fmt.Sprintf("%s: %v", key, err)}
		}
		v.Line = n
		cur.Keys = append(cur.Keys, Key{Name: key, Value: v})
	}
	return d, nil
}

// stripComment убирает комментарий: «#» вне кавычек.
func stripComment(line string) string {
	in := false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && in:
			i++
		case c == '"':
			in = !in
		case c == '#' && !in:
			return line[:i]
		}
	}
	return line
}

func validName(s string, dots bool) bool {
	if s == "" || s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		case r == '.' && dots:
		default:
			return false
		}
	}
	return true
}

func parseValue(s string) (Value, error) {
	switch {
	case s == "":
		return Value{}, fmt.Errorf("нет значения")
	case s == "true" || s == "false":
		return Value{Kind: KindBool, Bool: s == "true"}, nil
	case s[0] == '"':
		str, rest, err := parseString(s)
		if err != nil {
			return Value{}, err
		}
		if strings.TrimSpace(rest) != "" {
			return Value{}, fmt.Errorf("лишнее после строки: %q", rest)
		}
		return Value{Kind: KindString, Str: str}, nil
	case s[0] == '[':
		return parseList(s)
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil {
		return Value{}, fmt.Errorf("непонятное значение %q (строки пишутся в кавычках)", s)
	}
	return Value{Kind: KindNumber, Num: f, Raw: s}, nil
}

// parseString разбирает строку в кавычках в начале s; возвращает её и остаток.
func parseString(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("строка оборвана")
			}
			i++
			switch s[i] {
			case '"', '\\':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", "", fmt.Errorf("неизвестное экранирование \\%c", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("нет закрывающей кавычки")
}

func parseList(s string) (Value, error) {
	if !strings.HasSuffix(s, "]") {
		return Value{}, fmt.Errorf("список должен заканчиваться «]» в той же строке")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	v := Value{Kind: KindList, List: []string{}}
	for inner != "" {
		if inner[0] != '"' {
			return Value{}, fmt.Errorf("в списке — только строки в кавычках")
		}
		str, rest, err := parseString(inner)
		if err != nil {
			return Value{}, err
		}
		v.List = append(v.List, str)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return Value{}, fmt.Errorf("элементы списка разделяются запятой")
		}
		inner = strings.TrimSpace(rest[1:])
	}
	return v, nil
}

// Quote записывает строку в кавычках с экранированием.
func Quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// QuoteList записывает список строк.
func QuoteList(l []string) string {
	parts := make([]string, len(l))
	for i, s := range l {
		parts[i] = Quote(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
