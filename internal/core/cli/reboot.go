package cli

import (
	"errors"
	"os/exec"
)

var rebootCmd = Command{
	Name:    "reboot",
	Summary: "корректно остановить движок и перезагрузить колонку",
	Usage:   "[-yes]",
	Help: `Останавливает движок так же, как openmediad stop (музыка встаёт на паузу и переходит к штатному mediad), сбрасывает
данные на диск (sync) и перезагружает колонку. Если openmediad установлен и автозапуск включён, после загрузки движок
запустится сам.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "reboot")
		yes := fs.Bool("yes", false, "не спрашивать подтверждение")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := requireSpeaker(); err != nil {
			return err
		}
		if !*yes && !ask(c, "Перезагрузить колонку?") {
			return errors.New("отменено")
		}
		if guardRunning() != 0 || daemonPID() != 0 {
			if err := stop(c, false); err != nil {
				c.Printf("остановка: %v — перезагружаю всё равно\n", err)
			}
		}
		exec.Command("sync").Run()
		c.Printf("перезагрузка…\n")
		if out, err := exec.Command("reboot").CombinedOutput(); err != nil {
			return errors.New("reboot: " + err.Error() + ": " + string(out))
		}
		return nil
	},
}
