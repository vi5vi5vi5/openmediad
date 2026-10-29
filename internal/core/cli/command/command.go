// Package command — как устроена команда терминала `openmediad ...` и как добавить свою. Отдельный маленький пакет, чтобы
// команду могли принести не только сами команды ядра (internal/core/cli), но и компоненты и библиотеки: они регистрируют
// команду в init() через Register, а ядро собирает справку из того, что зарегистрировано. Так `openmediad bt` появляется,
// только если в сборку вошёл пакет, который эту команду приносит (internal/libs/bt-command), и исчезает вместе с ним.
package command

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Command — одна команда.
type Command struct {
	Name    string
	Aliases []string
	Summary string // строка для общего списка
	Usage   string // аргументы после имени, например "[-dry-run] [-yes]"
	Help    string // подробности для `openmediad help <команда>`
	Hidden  bool   // не показывать в общем списке
	Group   string // заголовок группы в общем списке (задаётся при регистрации)
	Run     func(c *Ctx, args []string) error
}

// Ctx — окружение команды (подменяется в тестах).
type Ctx struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Printf печатает в стандартный вывод команды.
func (c *Ctx) Printf(format string, a ...any) { fmt.Fprintf(c.Stdout, format, a...) }

// UsageError — неверные аргументы: код выхода 2 и подсказка по команде.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

// ErrNotDevice — команда работает только на колонке: код выхода 3.
var ErrNotDevice = errors.New("эта команда работает только на колонке")

// ErrSilent — код выхода 1 без сообщения (всё уже напечатано).
var ErrSilent = errors.New("")

var (
	mu         sync.Mutex
	registered []Command
)

// Register добавляет команду в группу справки. Зовётся из init(). Команда с уже занятым именем игнорируется:
// так два пакета могут приносить одну общую команду.
func Register(group string, cmd Command) {
	mu.Lock()
	defer mu.Unlock()
	for _, c := range registered {
		if c.Name == cmd.Name {
			return
		}
	}
	cmd.Group = group
	registered = append(registered, cmd)
}

// All — зарегистрированные команды в порядке регистрации.
func All() []Command {
	mu.Lock()
	defer mu.Unlock()
	return append([]Command(nil), registered...)
}

// Find ищет команду по имени или псевдониму.
func Find(name string) *Command {
	mu.Lock()
	defer mu.Unlock()
	for i := range registered {
		c := &registered[i]
		if c.Name == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// Flags создаёт набор флагов команды: ошибки разбора возвращаются как UsageError, помощь — как flag.ErrHelp.
func Flags(c *Ctx, cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	fs.Usage = func() {
		if f := strings.Fields(cmd); len(f) > 0 {
			if k := Find(f[0]); k != nil { // «bt scan» → справка по «bt»
				PrintCommandHelp(c.Stderr, k)
			}
		}
	}
	return fs
}

// Parse разбирает флаги и проверяет, что лишних аргументов не больше maxArgs (-1 — сколько угодно).
func Parse(fs *flag.FlagSet, args []string, maxArgs int) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return UsageError{Msg: err.Error()}
	}
	if maxArgs >= 0 && fs.NArg() > maxArgs {
		return UsageError{Msg: "лишние аргументы: " + strings.Join(fs.Args()[maxArgs:], " ")}
	}
	return nil
}

// PrintCommandHelp печатает справку по одной команде.
func PrintCommandHelp(w io.Writer, c *Command) {
	fmt.Fprintf(w, "openmediad %s %s\n  %s\n", c.Name, c.Usage, c.Summary)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "  другие имена: %s\n", strings.Join(c.Aliases, ", "))
	}
	if c.Help != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.Help))
	}
}
