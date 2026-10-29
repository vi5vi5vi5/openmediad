package config

import (
	"strings"

	"openmediad/internal/core/registry"
)

// coreTemplate — начало файла настроек по умолчанию: секции ядра с пояснениями.
const coreTemplate = `# Настройки openmediad — медиа-движка колонки.
# Правка с проверкой: openmediad config. Только проверка: openmediad config check. Один ключ: openmediad config set КЛЮЧ ЗНАЧЕНИЕ.
# Строки — в кавычках, списки — в квадратных скобках, комментарии — после #. После правки движок перечитает настройки
# (то, что нельзя применить на лету, применится после openmediad restart).

[general]
mode = "shim"            # shim — штатный mediad работает рядом и подхватывает всё, что openmediad не умеет
                         # replace — только openmediad (появится в фазе 12 плана)
log_level = "info"       # trace | info | warn | error — что сохранять в журнале (openmediad logs)
log_entries = 2000       # сколько записей журнала держать в памяти
log_file = ""            # файл журнала на /data, например "/data/opt/openmediad/log/openmediad.log";
                         # пусто — только в памяти (флеш не изнашивается)
log_file_max_kb = 1024   # предел файла журнала: старая половина уходит в .1

[fallback]               # штатный mediad всегда работает рядом и подхватывает всё, что openmediad не смог: ошибку
                         # до первого звука, выключенный компонент, падение демона. Совсем вернуться к штатному
                         # плееру: openmediad stop (до перезагрузки) или openmediad disable (насовсем)
max_crashes = 3          # после стольких быстрых падений подряд движок не запускается до перезагрузки (колонка штатная)

[route]                  # куда идёт звук каждого класса: первый доступный выход из списка
music = ["speaker"]      # музыка; например ["bt-out", "speaker"] — наушники, а если их нет — динамики
voice = ["speaker"]      # голос Алисы
alert = ["speaker"]      # звонки таймеров и будильников

[focus]
duck_gain = 0.25         # громкость музыки, пока Алиса слушает или говорит (0 — тишина, 1 — без приглушения)

[speaker]                # динамики колонки
device = "default"       # устройство ALSA (общий смеситель колонки)
aplay = "/usr/bin/aplay" # системный 32-битный aplay: свой вывод только через него (см. docs/ARCHITECTURE.md)
`

// Template — файл настроек по умолчанию для этой сборки: ядро и секции её компонентов.
func Template() string {
	var b strings.Builder
	b.WriteString(coreTemplate)
	for _, c := range registry.All() {
		if t := strings.TrimSpace(c.Template); t != "" {
			b.WriteString("\n" + t + "\n")
		}
	}
	return b.String()
}
