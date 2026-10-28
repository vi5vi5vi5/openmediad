package cli

import (
	"encoding/json"
	"fmt"

	"openmediad/internal/control"
	"openmediad/internal/journal"
)

var logsCmd = Command{
	Name:    "logs",
	Summary: "журнал демона из памяти",
	Usage:   "[-f] [-n 100] [-level info] [-why] [-json]",
	Help: `Последние записи журнала работающего демона. С -f — и новые по мере появления (выход — Ctrl+C).

Уровни: trace (каждый кадр шины), info, warn, error. Журнал хранит записи не ниже general.log_level из настроек;
-level здесь только отбирает из того, что сохранено.

С -why под записью печатается пояснение простыми словами (то же, что на отладочной странице).`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "logs")
		follow := fs.Bool("f", false, "следить за новыми записями")
		n := fs.Int("n", 100, "сколько последних записей показать (0 — все)")
		level := fs.String("level", "", "не ниже этого уровня: trace | info | warn | error")
		why := fs.Bool("why", false, "печатать пояснения")
		asJSON := fs.Bool("json", false, "записи строками JSON")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if *level != "" && !journal.ValidLevel(*level) {
			return UsageError{Msg: fmt.Sprintf("неизвестный уровень %q", *level)}
		}
		print := func(e journal.Entry) {
			if *asJSON {
				b, _ := json.Marshal(e)
				c.printf("%s\n", b)
				return
			}
			c.printf("%s\n", e.Line())
			if *why && e.Why != "" {
				c.printf("      └ %s\n", e.Why)
			}
		}
		req := control.Request{Cmd: "logs", Limit: *n, Level: *level}
		if !*follow {
			var es []journal.Entry
			if err := control.Call(control.SocketPath(), req, &es); err != nil {
				return err
			}
			for _, e := range es {
				print(e)
			}
			return nil
		}
		req.Cmd = "follow"
		return control.Stream(control.SocketPath(), req, func(data json.RawMessage) bool {
			var e journal.Entry
			if json.Unmarshal(data, &e) == nil {
				print(e)
			}
			return true
		})
	},
}
