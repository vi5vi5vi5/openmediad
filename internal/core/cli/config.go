package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/config"
)

var configCmd = Command{
	Name:    "config",
	Summary: "настройки: открыть в редакторе, проверить, прочитать или поменять ключ",
	Usage:   "[check [ФАЙЛ] | get КЛЮЧ | set КЛЮЧ ЗНАЧЕНИЕ | show | path | reset [-yes]]",
	Help: `Без аргументов — открыть настройки в редакторе ($EDITOR, иначе nano, иначе vi). После выхода файл проверяется:
при ошибке показывается строка и причина и предлагается исправить; если всё верно — файл сохраняется, а работающий
демон перечитывает настройки (что нельзя применить на ходу, применится после openmediad restart).

  check [ФАЙЛ]       проверить файл настроек (по умолчанию — действующий)
  get КЛЮЧ           значение ключа с учётом умолчаний: openmediad config get general.log_level
  set КЛЮЧ ЗНАЧЕНИЕ  поменять один ключ (комментарии в файле сохраняются):
                       openmediad config set debug-page.enabled false
                       openmediad config set general.log_level trace
                       openmediad config set route.music '["bt-out", "speaker"]'
  show               текст файла настроек (если файла нет — файл по умолчанию)
  path               где лежит файл (другой — переменная OPENMEDIAD_CONFIG)
  reset [-yes]       вернуть файл по умолчанию (старый сохраняется рядом с .bak)

Ключ — это секция и имя через точку: [yandex-music] enabled → yandex-music.enabled.`,
	Run: func(c *Ctx, args []string) error {
		if len(args) == 0 {
			return editConfig(c, config.Path())
		}
		sub, rest := args[0], args[1:]
		switch sub {
		case "check":
			return configCheck(c, rest)
		case "get":
			return configGet(c, rest)
		case "set":
			return configSet(c, rest)
		case "show":
			if len(rest) > 0 {
				return UsageError{Msg: "у show нет аргументов"}
			}
			text, _, err := config.ReadText(config.Path())
			if err != nil {
				return err
			}
			c.Printf("%s", text)
			return nil
		case "path":
			c.Printf("%s\n", config.Path())
			return nil
		case "reset":
			return configReset(c, rest)
		case "-h", "--help", "help":
			printCommandHelp(c.Stdout, find("config"))
			return nil
		}
		return UsageError{Msg: fmt.Sprintf("неизвестное действие %q", sub)}
	},
}

func configCheck(c *Ctx, args []string) error {
	if len(args) > 1 {
		return UsageError{Msg: "check принимает не больше одного файла"}
	}
	path := config.Path()
	if len(args) == 1 {
		path = args[0]
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		c.Printf("%s: файла нет — действуют умолчания (openmediad config создаст файл)\n", path)
		return nil
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings {
		c.Printf("внимание: %s\n", w)
	}
	c.Printf("%s: настройки верны\n", path)
	return nil
}

func configGet(c *Ctx, args []string) error {
	if len(args) != 1 {
		return UsageError{Msg: "нужен один ключ, например general.log_level"}
	}
	cfg, err := config.LoadFile(config.Path())
	if err != nil {
		return err
	}
	v, err := config.Get(cfg, args[0])
	if err != nil {
		return err
	}
	c.Printf("%s\n", v)
	return nil
}

func configSet(c *Ctx, args []string) error {
	if len(args) != 2 {
		return UsageError{Msg: "нужны ключ и значение, например debug-page.enabled false"}
	}
	return command.SetConfig(c, args[0], args[1])
}

func configReset(c *Ctx, args []string) error {
	fs := flags(c, "config")
	yes := fs.Bool("yes", false, "не спрашивать подтверждение")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	path := config.Path()
	old, exists, err := config.ReadText(path)
	if err != nil {
		return err
	}
	if exists {
		if !*yes && !ask(c, "Заменить "+path+" файлом по умолчанию? Старый останется в .bak") {
			return errors.New("отменено")
		}
		if err := config.WriteFile(path+".bak", old); err != nil {
			return err
		}
	}
	if err := config.WriteFile(path, config.Template()); err != nil {
		return err
	}
	c.Printf("записан файл по умолчанию: %s\n", path)
	applyConfig(c)
	return nil
}

// applyConfig просит работающий демон перечитать настройки и печатает, что применилось.
func applyConfig(c *Ctx) { command.ReloadDaemon(c) }

// configStamp — время изменения файла (чтобы заметить, что редактор ничего не сохранил).
func configStamp(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// configPathHint — путь к настройкам для текста помощи.
func configPathHint() string { return config.Path() }
