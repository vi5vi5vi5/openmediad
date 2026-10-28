package cli

import (
	"context"
	"os"

	"openmediad/internal/app"
)

var runCmd = Command{
	Name:    "run",
	Summary: "сам демон (его запускает страж; вручную — для отладки)",
	Usage:   "[-config ФАЙЛ] [-listen АДРЕС] [-upstream АДРЕС] [-src АДРЕС]",
	Help: `Запускает движок в этом терминале и работает до Ctrl+C или SIGTERM. Журнал копируется в терминал.

Обычно демон запускает страж (openmediad start или автозапуск): он же ставит правило iptables, без которого платформа
не идёт через openmediad. Ручной run без правила — шим ждёт соединений, но их никто не присылает.

SIGHUP перечитывает настройки (как openmediad config после правки).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "run")
		var o app.Options
		fs.StringVar(&o.ConfigPath, "config", "", "файл настроек (по умолчанию "+configPathHint()+")")
		fs.StringVar(&o.Listen, "listen", app.DefaultListen, "где слушать платформу")
		fs.StringVar(&o.Upstream, "upstream", app.DefaultUpstream, "штатный mediad")
		fs.StringVar(&o.Src, "src", app.DefaultSrc, "адрес исходящих соединений к mediad (пусто — системный)")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		o.Foreground = isTerminal(os.Stderr)
		return app.Run(context.Background(), o)
	},
}

// isTerminal — вывод идёт в терминал (а не в файл или /dev/null).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
