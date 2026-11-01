package cli

import (
	"errors"
	"fmt"
	"os"

	"openmediad/internal/core/config"
)

// configUpdate применяет файл настроек без перезапуска движка: проверяет его так же, как демон, и отдаёт работающему демону.
func configUpdate(c *Ctx, args []string) error {
	if len(args) > 0 {
		return UsageError{Msg: "у update нет аргументов"}
	}
	path := config.Path()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		c.Printf("%s: файла нет — действуют умолчания (openmediad config создаст файл)\n", path)
		return nil
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		return fmt.Errorf("%w\nдемону настройки не отданы: исправьте файл (openmediad config) и повторите", err)
	}
	for _, w := range cfg.Warnings {
		c.Printf("внимание: %s\n", w)
	}
	applyConfig(c)
	return nil
}

// configUpgrade дописывает в файл настроек недостающие ключи и секции.
func configUpgrade(c *Ctx, args []string) error {
	fs := flags(c, "config upgrade")
	dry := fs.Bool("n", false, "только показать, что будет добавлено")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	path := config.Path()
	text, exists, err := config.ReadText(path)
	if err != nil {
		return err
	}
	if !exists {
		c.Printf("%s: файла нет — действуют умолчания, дописывать нечего (openmediad config создаст полный файл)\n", path)
		return nil
	}
	out, added, err := config.Upgrade(text)
	if err != nil {
		return err
	}
	if len(added) == 0 {
		c.Printf("%s: в файле есть все ключи этой сборки\n", path)
		return nil
	}
	for _, a := range added {
		c.Printf("  + %s\n", a)
	}
	if *dry {
		c.Printf("пробный прогон: файл не менялся (без -n будет дописано ключей: %d)\n", len(added))
		return nil
	}
	if err := config.WriteFile(path+".bak", text); err != nil {
		return err
	}
	if err := config.WriteFile(path, out); err != nil {
		return err
	}
	c.Printf("дописано ключей: %d. Прежние значения и комментарии не тронуты, старый файл — %s.bak\n", len(added), path)
	c.Printf("Новые ключи получили умолчания, поведение не изменилось. Правьте нужные и примените: openmediad config update\n")
	return nil
}
