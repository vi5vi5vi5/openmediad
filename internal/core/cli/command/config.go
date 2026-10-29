package command

import (
	"errors"

	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
)

// SetConfig меняет один ключ файла настроек (комментарии сохраняются, результат проверяется целиком) и просит работающий
// демон перечитать настройки. Общая реализация `openmediad config set` и команд, которые пишут настройки (bt use).
func SetConfig(c *Ctx, key, value string) error {
	path := config.Path()
	text, _, err := config.ReadText(path)
	if err != nil {
		return err
	}
	out, err := config.Set(text, key, value)
	if err != nil {
		return err
	}
	if out == text {
		c.Printf("%s уже %s\n", key, value)
		return nil
	}
	if err := config.WriteFile(path, out); err != nil {
		return err
	}
	v, _ := config.Get(mustLoad(out), key)
	c.Printf("%s = %s\n", key, v)
	ReloadDaemon(c)
	return nil
}

// ReloadDaemon просит работающий демон перечитать настройки и печатает, что применилось.
func ReloadDaemon(c *Ctx) {
	var msg string
	err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "reload"}, &msg)
	switch {
	case errors.Is(err, ctlsock.ErrNotRunning):
		c.Printf("демон не запущен: настройки применятся при запуске\n")
	case err != nil:
		c.Printf("демон не принял настройки: %v\n", err)
	default:
		c.Printf("%s\n", msg)
	}
}

func mustLoad(text string) *config.Config {
	cfg, err := config.Load(text)
	if err != nil {
		return config.Defaults()
	}
	return cfg
}
