package cli

import "openmediad/internal/core/cli/command"

// Группы команд ядра в порядке показа в справке. Команды компонентов и библиотек добавляются в свои группы через
// command.Register из их init() и показываются после этих групп.
const (
	groupHelp    = "справка"
	groupState   = "состояние и настройки"
	groupRunning = "включить и выключить до перезагрузки"
	groupInstall = "установка на колонку"
	groupService = "проверка и обслуживание"
	groupGuard   = "для стража"
)

var coreGroups = []string{groupHelp, groupState, groupRunning, groupInstall, groupService, groupGuard}

// Новая команда ядра: свой файл и строка здесь. Порядок внутри группы — порядок в справке.
func init() {
	for _, g := range []struct {
		title string
		cmds  []Command
	}{
		{groupHelp, []Command{helpCmd, versionCmd}},
		{groupState, []Command{statusCmd, logsCmd, configCmd, componentsCmd}},
		{groupRunning, []Command{startCmd, stopCmd, restartCmd}},
		{groupInstall, []Command{installCmd, uninstallCmd, enableCmd, disableCmd}},
		{groupService, []Command{doctorCmd, testSoundCmd, rebootCmd}},
		{groupGuard, []Command{runCmd, rerouteCmd}},
	} {
		for _, c := range g.cmds {
			command.Register(g.title, c)
		}
	}
}
