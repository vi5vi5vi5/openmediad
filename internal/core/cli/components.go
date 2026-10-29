package cli

import (
	"errors"
	"fmt"

	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/registry"
)

var componentsCmd = Command{
	Name:    "components",
	Summary: "компоненты: паспорт, в сборке ли, включены в настройках, работают сейчас",
	Usage:   "[-all] [-v]",
	Help: `Таблица компонентов этой сборки: класс (необходимый / штатный / вспомогательный / необязательный), входит ли в сборку по
умолчанию, вошёл ли в бинарник, включён ли в настройках, работает ли в демоне сейчас.
С -all — ещё компоненты, которые в сборку не вошли, и запланированные (с номером фазы плана).
С -v — под каждым компонентом ещё и то, что он требует от прошивки и от других компонентов, и что будет, если его убрать.

Компонент, не вошедший в сборку, добавляется пересборкой: go run ./tools/build -with ИМЯ (см. README).
Включить или выключить вошедший: openmediad config set yandex-music.enabled false (имя секции совпадает с именем компонента).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "components")
		all := fs.Bool("all", false, "показать и то, что не вошло в сборку")
		verbose := fs.Bool("v", false, "с зависимостями и последствиями отключения")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		cfg, cfgErr := config.LoadFile(config.Path())
		if cfgErr != nil {
			c.Printf("настройки не прочитаны (%v): колонка «включён» — по умолчаниям\n\n", cfgErr)
			cfg = config.Defaults()
		}
		var st ctlsock.Status
		err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "status"}, &st)
		running := map[string]ctlsock.ComponentStatus{}
		for _, cs := range st.Components {
			running[cs.Name] = cs
		}
		daemon := err == nil
		if err != nil && !errors.Is(err, ctlsock.ErrNotRunning) {
			return err
		}

		c.Printf("%-17s %-16s %-12s %-8s %-9s %-10s %s\n", "компонент", "класс", "по умолчанию", "сборка", "настройки", "сейчас", "что делает")
		for _, info := range registry.Catalog {
			_, built := registry.Get(info.Name)
			if !built && !*all {
				continue
			}
			inBuild, enabled, now := "нет", "—", "—"
			def := "нет"
			if info.Default {
				def = "да"
			}
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
				inBuild, def = fmt.Sprintf("фаза %d", info.Phase), "—"
			}
			c.Printf("%-17s %-16s %-12s %-8s %-9s %-10s %s\n", info.Name, info.Class, def, inBuild, enabled, now, info.Summary)
			if *verbose {
				printPassport(c, info)
			}
		}
		if !daemon {
			c.Printf("\nдемон не запущен: столбец «сейчас» пуст\n")
		}
		if !*all {
			if m := registry.Missing(); len(m) > 0 {
				c.Printf("\nне вошли в сборку (готовы): %s — openmediad components -all\n", listOrNone(m))
			}
		}
		return nil
	},
}

// printPassport — подробности паспорта под строкой компонента.
func printPassport(c *Ctx, info registry.Info) {
	if info.Experimental {
		c.Printf("    опытный: проверен не полностью\n")
	}
	for _, n := range info.Needs {
		c.Printf("    нужно от прошивки: %s\n", n.Text)
	}
	if len(info.NeedsComponents) > 0 {
		c.Printf("    нужно от компонентов: %s\n", listOrNone(info.NeedsComponents))
	}
	if info.WithoutIt != "" {
		c.Printf("    если убрать: %s\n", info.WithoutIt)
	}
}

func onOff(b bool) string {
	if b {
		return "включён"
	}
	return "выключен"
}

func nowState(cs ctlsock.ComponentStatus) string {
	switch {
	case cs.Error != "":
		return "ошибка"
	case cs.Running:
		return "работает"
	}
	return "—"
}
