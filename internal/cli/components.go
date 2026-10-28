package cli

import (
	"errors"
	"fmt"

	"openmediad/internal/app"
	"openmediad/internal/config"
	"openmediad/internal/control"
	"openmediad/internal/registry"
)

var componentsCmd = Command{
	Name:    "components",
	Summary: "компоненты: в сборке, включены в настройках, работают сейчас",
	Usage:   "[-all]",
	Help: `Таблица компонентов этой сборки: вошёл ли в бинарник, включён ли в настройках, работает ли в демоне сейчас.
С -all — ещё компоненты, которые в сборку не вошли, и запланированные (с номером фазы плана).

Компонент, не вошедший в сборку, добавляется пересборкой: go run ./tools/build -with ИМЯ (см. README).
Включить или выключить вошедший: openmediad config set source.yandex.enabled false (имя секции — в config).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "components")
		all := fs.Bool("all", false, "показать и то, что не вошло в сборку")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		cfg, cfgErr := config.LoadFile(config.Path())
		if cfgErr != nil {
			c.printf("настройки не прочитаны (%v): колонка «включён» — по умолчаниям\n\n", cfgErr)
			cfg = config.Defaults()
		}
		var st app.Status
		err := control.Call(control.SocketPath(), control.Request{Cmd: "status"}, &st)
		running := map[string]app.ComponentStatus{}
		for _, cs := range st.Components {
			running[cs.Name] = cs
		}
		daemon := err == nil
		if err != nil && !errors.Is(err, control.ErrNotRunning) {
			return err
		}

		c.printf("%-11s %-9s %-8s %-9s %-10s %s\n", "компонент", "вид", "сборка", "настройки", "сейчас", "что делает")
		for _, info := range registry.Catalog {
			_, built := registry.Get(info.Name)
			if !built && !*all {
				continue
			}
			inBuild, enabled, now := "нет", "—", "—"
			switch {
			case built:
				inBuild = "да"
				enabled = onOff(cfg.Enabled(info.Name))
				if info.Section == "" {
					enabled = "всегда"
				}
				if daemon {
					now = nowState(running[info.Name])
				}
			case !info.Ready:
				inBuild = fmt.Sprintf("фаза %d", info.Phase)
			}
			c.printf("%-11s %-9s %-8s %-9s %-10s %s\n", info.Name, info.Kind, inBuild, enabled, now, info.Summary)
		}
		if !daemon {
			c.printf("\nдемон не запущен: столбец «сейчас» пуст\n")
		}
		if !*all {
			if m := registry.Missing(); len(m) > 0 {
				c.printf("\nне вошли в сборку (готовы): %s — openmediad components -all\n", listOrNone(m))
			}
		}
		return nil
	},
}

func onOff(b bool) string {
	if b {
		return "включён"
	}
	return "выключен"
}

func nowState(cs app.ComponentStatus) string {
	switch {
	case cs.Error != "":
		return "ошибка"
	case cs.Running:
		return "работает"
	}
	return "—"
}
