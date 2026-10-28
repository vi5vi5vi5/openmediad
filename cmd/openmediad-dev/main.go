// openmediad-dev — инструменты разработчика openmediad: наблюдение за шиной, разбор записей трафика, сверка шима, замеры
// звука, обезличивание записей. В продукт (openmediad) не входят.
//
// Сборка: go run ./tools/build -dev (под колонку, -deploy адрес — залить в /tmp/openmediad-dev) или go run ./cmd/openmediad-dev
// на компьютере (разбор записей).
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

type command struct {
	name, usage, summary string
	run                  func(args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"observe", "[-aliced адрес] [-audio адрес] [-progress] [-noisy] [-stats 10s] [-mono] [-bind адрес]",
			"на колонке: подключиться к сервисам как обычный клиент и печатать их рассылки (ничего не отправляет)", observeCmd},
		{"pcap", "[-progress] [-noisy] [-dump [-dump-max 160] [-dump-types 8,127]] [-voice-dir каталог] запись|-",
			"разбор записи (tcpdump -i lo или .omdf) обоих направлений; -voice-dir сохраняет голос Алисы файлами .ogg", pcapCmd},
		{"timeline", "[-progress] [-voice] запись",
			"сжатая шкала команд и событий плеера — то, что видела платформа", timelineCmd},
		{"shimcheck", "[-client 19906] [-service 9906] [-src 127.0.0.2] файл.pcap",
			"сверка кадров обеих сторон шима и добавляемой им задержки по записи tcpdump", shimCheckCmd},
		{"pcmwatch", "[-for 60s]",
			"на колонке: смены состояния PCM и trigger_time (запуск потока ядром) — шкала для замеров задержки", pcmWatchCmd},
		{"bt-idle", "-pid PID [-state idle] АДРЕС",
			"опыт: адресно сообщить платформе, что транспорт A2DP устройства простаивает", btIdleCmd},
		{"mixer", "[-watch 30s] [регулятор]",
			"на колонке: регулятор ALSA (по умолчанию Master) через управляющее устройство и его изменения — без amixer", mixerCmd},
		{"sanitize", "[-keep-titles] [-keep-voice] запись выход.omdf",
			"обезличить запись для testdata/: заменить ссылки, ключи, идентификаторы запросов, названия, голос", sanitizeCmd},
	}
	commands = append(commands, audioCommands()...)
}

func main() {
	// Инструменты запускаются и на колонке: частая сборка мусора дешевле лишних мегабайт.
	debug.SetGCPercent(25)
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage(os.Stdout)
		return
	}
	for _, c := range commands {
		if c.name == os.Args[1] {
			if err := c.run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "openmediad-dev %s: %v\n", c.name, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n", os.Args[1])
	usage(os.Stderr)
	os.Exit(2)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "openmediad-dev — инструменты разработчика openmediad")
	fmt.Fprintln(w, "\nиспользование:")
	for _, c := range commands {
		fmt.Fprintf(w, "  openmediad-dev %s %s\n      %s\n", c.name, c.usage, c.summary)
	}
	fmt.Fprintln(w, "\nзаписи: tcpdump (.pcap) или обезличенный журнал кадров (.omdf, см. sanitize)")
}

// usageError — неверные аргументы команды.
func usageError(name string) error {
	for _, c := range commands {
		if c.name == name {
			return fmt.Errorf("использование: openmediad-dev %s %s", name, strings.TrimSpace(c.usage))
		}
	}
	return fmt.Errorf("неверные аргументы")
}
