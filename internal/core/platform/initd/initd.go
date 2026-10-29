// Package initd — шаблоны стража (guard.sh) и скрипта автозапуска (/etc/init.d/S98openmediad), которые бинарник пишет
// на колонку. Шаблоны лежат рядом обычными файлами и встраиваются в бинарник: так их удобно читать и проверять.
package initd

import (
	_ "embed"
	"strings"
)

//go:embed guard.sh
var guardTemplate string

//go:embed initd.sh
var initTemplate string

// GuardPaths — что подставляется в страж.
type GuardPaths struct {
	Bin   string // бинарник openmediad
	Run   string // рабочий каталог в ОЗУ: pid, журнал стража, счётчик падений
	State string // постоянные отметки (автозапуск выключен)
}

// Guard — текст стража для этих путей.
func Guard(p GuardPaths) string {
	return strings.NewReplacer("@BIN@", p.Bin, "@RUN@", p.Run, "@STATE@", p.State).Replace(guardTemplate)
}

// Init — текст скрипта автозапуска; home — каталог установки на колонке (/data/opt/openmediad).
func Init(home string) string {
	return strings.ReplaceAll(initTemplate, "@HOME@", home)
}
