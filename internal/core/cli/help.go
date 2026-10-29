package cli

import (
	"fmt"
	"io"
	"strings"

	"openmediad/internal/core/buildinfo"
	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/registry"
)

const tagline = "медиа-движок для Яндекс Станции Мини 3"

var helpCmd = Command{
	Name:    "help",
	Summary: "помощь по командам",
	Usage:   "[команда]",
	Run: func(c *Ctx, args []string) error {
		if len(args) == 0 {
			printHelp(c.Stdout)
			return nil
		}
		k := find(args[0])
		if k == nil {
			return UsageError{Msg: fmt.Sprintf("неизвестная команда %q", args[0])}
		}
		printCommandHelp(c.Stdout, k)
		return nil
	},
}

var versionCmd = Command{
	Name:    "version",
	Aliases: []string{"--version"},
	Summary: "версия одной строкой",
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "version")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		i := buildinfo.Get()
		c.Printf("%s %s\n", i.Line(), i.Target)
		return nil
	},
}

// printHelp печатает шапку со сведениями о сборке и список команд.
func printHelp(w io.Writer) {
	i := buildinfo.Get()
	fmt.Fprintf(w, "%s — %s\n%s\n", i.Line(), tagline, i.Build())
	fmt.Fprintf(w, "компоненты: %s\n", listOrNone(registry.Compiled()))
	if m := registry.Missing(); len(m) > 0 {
		fmt.Fprintf(w, "не вошли в сборку: %s\n", strings.Join(m, " "))
	}
	fmt.Fprintf(w, "\nиспользование: openmediad <команда> [флаги]\n\n")
	all := command.All()
	width := 0
	for _, c := range all {
		if !c.Hidden {
			width = max(width, len([]rune(c.Name)))
		}
	}
	first := true
	for _, group := range groupOrder(all) {
		var lines []Command
		for _, c := range all {
			if c.Group == group && !c.Hidden {
				lines = append(lines, c)
			}
		}
		if len(lines) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintf(w, "%s:\n", group)
		for _, c := range lines {
			fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
		}
	}
	fmt.Fprintf(w, "\nподробно: openmediad help <команда>\n")
}

// groupOrder — группы справки: сначала группы ядра, потом группы команд, которые принесли компоненты (в порядке появления).
func groupOrder(all []Command) []string {
	order := append([]string(nil), coreGroups...)
	seen := map[string]bool{}
	for _, g := range coreGroups {
		seen[g] = true
	}
	for _, c := range all {
		if !seen[c.Group] {
			seen[c.Group] = true
			order = append(order, c.Group)
		}
	}
	return order
}

func printCommandHelp(w io.Writer, c *Command) { command.PrintCommandHelp(w, c) }

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "нет"
	}
	return strings.Join(s, " ")
}
