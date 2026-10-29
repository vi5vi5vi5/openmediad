package catalog

import (
	"fmt"
	"strings"

	"openmediad/internal/core/registry"
)

// Маркеры блока «Паспорт» в README.md компонента: всё между ними переписывает `go run ./tools/build -sync-docs`.
const (
	PassportBegin = "<!-- passport:begin — генерируется из internal/components/catalog: go run ./tools/build -sync-docs; руками не править -->"
	PassportEnd   = "<!-- passport:end -->"
)

// ClassNote — что значит класс.
func ClassNote(c registry.Class) string {
	switch c {
	case registry.Necessary:
		return "то, ради чего проект существует"
	case registry.Standard:
		return "повторяет штатное поведение колонки"
	case registry.Auxiliary:
		return "для людей, не для звука: отладка, разработка"
	}
	return "новая возможность сверх штатной колонки"
}

func yesNo(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

// sectionOf — как называется секция файла настроек компонента («—», если настроек нет).
func sectionOf(c registry.Info) string {
	if c.Section == "" {
		return "нет настроек"
	}
	return "`[" + c.Section + "]`"
}

func needsText(c registry.Info) string {
	if len(c.Needs) == 0 {
		return "—"
	}
	parts := make([]string, len(c.Needs))
	for i, n := range c.Needs {
		parts[i] = n.Text
	}
	return strings.Join(parts, "; ")
}

func needsComponentsText(c registry.Info) string {
	if len(c.NeedsComponents) == 0 {
		return "—"
	}
	parts := make([]string, len(c.NeedsComponents))
	for i, n := range c.NeedsComponents {
		parts[i] = "`" + n + "`"
	}
	return strings.Join(parts, ", ")
}

// PassportBlock — блок «Паспорт» (с маркерами) для README.md компонента.
func PassportBlock(c registry.Info) string {
	var b strings.Builder
	b.WriteString(PassportBegin + "\n")
	b.WriteString("| | |\n|---|---|\n")
	row := func(k, v string) { fmt.Fprintf(&b, "| %s | %s |\n", k, v) }
	row("Название", c.Title)
	row("Имя · тег сборки · настройки", fmt.Sprintf("`%s` · `%s` · %s", c.Name, c.Tag(), sectionOf(c)))
	row("Вид", c.Kind.String()+kindSuffix(c.Kind))
	row("Класс", fmt.Sprintf("**%s** — %s", c.Class, ClassNote(c.Class)))
	if c.Ready {
		row("В сборке по умолчанию", yesNo(c.Default)+defaultNote(c))
		row("Зрелость", maturity(c))
	} else {
		row("Состояние", fmt.Sprintf("запланирован, фаза %d плана (в сборку не входит)", c.Phase))
	}
	row("Что делает", c.Summary)
	row("Нужно от прошивки", needsText(c))
	row("Нужно от других компонентов", needsComponentsText(c))
	if c.WithoutIt != "" {
		row("Если убрать", c.WithoutIt)
	}
	b.WriteString(PassportEnd + "\n")
	return b.String()
}

func kindSuffix(k registry.Kind) string {
	switch k {
	case registry.Source:
		return " звука (откуда берётся)"
	case registry.Sink:
		return " звука (куда уходит)"
	}
	return " (не про звук напрямую)"
}

func defaultNote(c registry.Info) string {
	if c.Default {
		return " (профиль `standard`)"
	}
	return " (добавляется `-with " + c.Name + "` или профилем `full`)"
}

func maturity(c registry.Info) string {
	if c.Experimental {
		return "опытный: проверен не полностью"
	}
	return "проверен"
}

// ReplacePassport подставляет свежий блок «Паспорт» в текст README.md. ok=false — маркеров в тексте нет.
func ReplacePassport(readme string, c registry.Info) (out string, ok bool) {
	i := strings.Index(readme, "<!-- passport:begin")
	j := strings.Index(readme, PassportEnd)
	if i < 0 || j < i {
		return readme, false
	}
	j += len(PassportEnd)
	if j < len(readme) && readme[j] == '\n' {
		j++
	}
	return readme[:i] + PassportBlock(c) + readme[j:], true
}

// ComponentsDoc — полный текст docs/COMPONENTS.md.
func ComponentsDoc() string {
	var b strings.Builder
	b.WriteString("# Компоненты\n\n")
	b.WriteString("Файл создаётся автоматически из `internal/components/catalog` (`go run ./tools/build -sync-docs`) — руками не править.\n\n")
	b.WriteString("Компонент — опциональная возможность движка: своя папка в `internal/components/`, свой паспорт, своя секция в файле настроек.\n")
	b.WriteString("Любой компонент можно не включать в сборку, и колонка не сломается: что он делал, играет штатный `mediad` (в паспорте — строка «Если убрать»).\n\n")
	b.WriteString("## Классы\n\n| Класс | Смысл |\n|---|---|\n")
	for _, c := range []registry.Class{registry.Necessary, registry.Standard, registry.Auxiliary, registry.Optional} {
		fmt.Fprintf(&b, "| **%s** | %s |\n", c, ClassNote(c))
	}
	b.WriteString("\n## Профили сборки\n\n| Профиль | Что входит |\n|---|---|\n")
	b.WriteString("| `core` | только ядро: прозрачный посредник, колонка играет штатно |\n")
	std, _ := Profile(ProfileStandard)
	full, _ := Profile(ProfileFull)
	fmt.Fprintf(&b, "| `standard` (по умолчанию) | %s |\n", codeList(std))
	fmt.Fprintf(&b, "| `full` | %s |\n\n", codeList(full))
	b.WriteString("```sh\ngo run ./tools/build                       # профиль standard\ngo run ./tools/build -profile core          # только ядро\ngo run ./tools/build -profile full          # всё готовое\ngo run ./tools/build -with bt-out           # standard + компонент\ngo run ./tools/build -without debug-page    # standard без компонента\ngo run ./tools/build -list                  # эта таблица в терминале\n```\n\n")
	b.WriteString("## Сводка\n\n| Компонент | Название | Класс | По умолчанию | Нужно от компонентов | Тег сборки |\n|---|---|---|---|---|---|\n")
	for _, c := range All {
		def := yesNo(c.Ready && c.Default)
		if !c.Ready {
			def = fmt.Sprintf("план, фаза %d", c.Phase)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | `%s` |\n", c.Name, c.Title, c.Class, def, needsComponentsText(c), c.Tag())
	}
	b.WriteString("\n## Паспорта\n")
	for _, c := range All {
		fmt.Fprintf(&b, "\n### `%s`\n\n%s", c.Name, strings.Replace(strings.Replace(PassportBlock(c), PassportBegin+"\n", "", 1), PassportEnd+"\n", "", 1))
	}
	return b.String()
}

func codeList(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = "`" + n + "`"
	}
	return strings.Join(parts, ", ")
}
