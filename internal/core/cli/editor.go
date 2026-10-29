package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
)

// editConfig открывает копию настроек в редакторе и, если после правки файл верен, атомарно заменяет настройки.
// Неверный файл не сохраняется: показывается ошибка и предлагается вернуться в редактор с той же копией.
func editConfig(c *Ctx, path string) error {
	editor, err := findEditor()
	if err != nil {
		return err
	}
	text, exists, err := config.ReadText(path)
	if err != nil {
		return err
	}
	dir := ctlsock.RunDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "openmediad.conf.edit")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	for {
		before := configStamp(tmp)
		if err := runEditor(c, editor, tmp); err != nil {
			return fmt.Errorf("редактор %s: %w", editor[0], err)
		}
		b, err := os.ReadFile(tmp)
		if err != nil {
			return err
		}
		edited := string(b)
		if edited == text && exists {
			c.Printf("изменений нет\n")
			return nil
		}
		if edited == text && configStamp(tmp).Equal(before) {
			c.Printf("файл не сохранён в редакторе — ничего не записано\n")
			return nil
		}
		cfg, err := config.Load(edited)
		if err != nil {
			c.Printf("в настройках ошибка: %v\n", err)
			if ask(c, "Вернуться в редактор и исправить? (нет — выйти без сохранения)") {
				continue
			}
			return errors.New("настройки не сохранены")
		}
		for _, w := range cfg.Warnings {
			c.Printf("внимание: %s\n", w)
		}
		if err := config.WriteFile(path, edited); err != nil {
			return err
		}
		c.Printf("сохранено: %s\n", path)
		applyConfig(c)
		return nil
	}
}

// findEditor — $VISUAL, $EDITOR, иначе nano, иначе vi (на колонке vi есть в busybox).
func findEditor() ([]string, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(env)); len(f) > 0 {
			return f, nil
		}
	}
	for _, name := range []string{"nano", "vi"} {
		if p, err := exec.LookPath(name); err == nil {
			return []string{p}, nil
		}
	}
	return nil, errors.New("не найден редактор (nano или vi); укажите EDITOR=… или правьте ключи: openmediad config set КЛЮЧ ЗНАЧЕНИЕ")
}

func runEditor(c *Ctx, editor []string, file string) error {
	cmd := exec.Command(editor[0], append(editor[1:], file)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
