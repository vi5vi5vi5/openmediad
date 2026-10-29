// Package cli — команды бинарника openmediad: разбор аргументов, помощь и по файлу на команду.
//
// Без аргументов бинарник печатает помощь и ничего не запускает: демон стартует только командой `run` (её вызывает страж).
package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"openmediad/internal/core/cli/command"
)

// Типы команды живут в пакете command: так команду могут принести и компоненты (internal/libs/bt-command).
type (
	Command    = command.Command
	Ctx        = command.Ctx
	UsageError = command.UsageError
)

// ErrNotDevice — команда работает только на колонке: код выхода 3.
var ErrNotDevice = command.ErrNotDevice

// errSilent — код выхода 1 без сообщения (всё уже напечатано).
var errSilent = command.ErrSilent

// Коды выхода.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitNotHere = 3
)

// Main выполняет команду из args (без имени программы) и возвращает код выхода.
func Main(args []string) int {
	return run(&Ctx{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, args)
}

func run(c *Ctx, args []string) int {
	if len(args) == 0 {
		printHelp(c.Stdout)
		return ExitOK
	}
	name := args[0]
	switch name {
	case "-h", "--help", "-help":
		name = "help"
	case "--version", "-version", "-v":
		name = "version"
	}
	cmd := find(name)
	if cmd == nil {
		fmt.Fprintf(c.Stderr, "неизвестная команда %q\n\n", name)
		printHelp(c.Stderr)
		return ExitUsage
	}
	err := cmd.Run(c, args[1:])
	var ue UsageError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.Is(err, errSilent):
		return ExitError
	case errors.As(err, &ue):
		fmt.Fprintf(c.Stderr, "openmediad %s: %s\nиспользование: openmediad %s %s\n", cmd.Name, ue.Msg, cmd.Name, cmd.Usage)
		return ExitUsage
	case errors.Is(err, ErrNotDevice):
		fmt.Fprintf(c.Stderr, "openmediad %s: %v\n", cmd.Name, err)
		return ExitNotHere
	default:
		fmt.Fprintf(c.Stderr, "openmediad %s: %v\n", cmd.Name, err)
		return ExitError
	}
}

func find(name string) *Command { return command.Find(name) }

// flags создаёт набор флагов команды: ошибки разбора возвращаются как UsageError, помощь — как flag.ErrHelp.
func flags(c *Ctx, cmd string) *flag.FlagSet { return command.Flags(c, cmd) }

// parse разбирает флаги и проверяет, что лишних аргументов не больше maxArgs (-1 — сколько угодно).
func parse(fs *flag.FlagSet, args []string, maxArgs int) error {
	return command.Parse(fs, args, maxArgs)
}
