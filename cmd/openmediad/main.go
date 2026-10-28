// openmediad — медиа-движок для Яндекс Станции Мини 3. Без аргументов печатает помощь и сведения о сборке.
//
// Компоненты попадают в бинарник через файлы with_<имя>.go этого каталога: у каждого свой тег сборки
// (см. internal/registry/catalog.go). Собирать удобнее всего так: go run ./tools/build.
package main

import (
	"os"

	"openmediad/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
