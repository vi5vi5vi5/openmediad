package cli

// commands — все команды в порядке показа в помощи, по группам. Новая команда добавляется сюда и в свой файл.
var commands []Command

func init() {
	groups := []struct {
		title string
		cmds  []Command
	}{
		{"справка", []Command{helpCmd, versionCmd}},
		{"состояние и настройки", []Command{statusCmd, logsCmd, configCmd, componentsCmd}},
		{"включить и выключить до перезагрузки", []Command{startCmd, stopCmd, restartCmd}},
		{"установка на колонку", []Command{installCmd, uninstallCmd, enableCmd, disableCmd}},
		{"проверка и обслуживание", []Command{doctorCmd, testSoundCmd, rebootCmd}},
		{"для стража", []Command{runCmd, rerouteCmd}},
	}
	for _, g := range groups {
		for _, c := range g.cmds {
			c.Group = g.title
			commands = append(commands, c)
		}
	}
}
