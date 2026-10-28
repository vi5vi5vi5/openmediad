package cli

import (
	"fmt"
	"io"
	"strings"

	"openmediad/internal/buildinfo"
	"openmediad/internal/registry"
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
		c.printf("%s %s\n", i.Line(), i.Target)
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
	width := 0
	for _, c := range commands {
		if !c.Hidden {
			width = max(width, len([]rune(c.Name)))
		}
	}
	group := ""
	for _, c := range commands {
		if c.Hidden {
			continue
		}
		if c.Group != group {
			if group != "" {
				fmt.Fprintln(w)
			}
			group = c.Group
			fmt.Fprintf(w, "%s:\n", group)
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
	fmt.Fprintf(w, "\nподробно: openmediad help <команда>\n")
}

func printCommandHelp(w io.Writer, c *Command) {
	fmt.Fprintf(w, "openmediad %s %s\n  %s\n", c.Name, c.Usage, c.Summary)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "  другие имена: %s\n", strings.Join(c.Aliases, ", "))
	}
	if c.Help != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.Help))
	}
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "нет"
	}
	return strings.Join(s, " ")
}
