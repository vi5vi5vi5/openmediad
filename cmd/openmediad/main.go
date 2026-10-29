// openmediad — медиа-движок для Яндекс Станции Мини 3. Без аргументов печатает помощь и сведения о сборке.
//
// Здесь только склейка. Список компонентов (паспорта) подключает пакет catalog, а какие из них войдут в бинарник, решает
// пакет included по тегам сборки (omd_<имя>). Собирать нужно через tools/build: `go run ./tools/build` (профиль standard);
// голый `go build` без тегов даст только ядро — прозрачного посредника без компонентов.
package main

import (
	"os"

	_ "openmediad/internal/components/catalog"
	_ "openmediad/internal/components/included"
	"openmediad/internal/core/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
