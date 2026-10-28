// Package iptables — правило, которое направляет платформу через шим openmediad. Ставит и снимает его только страж
// (guard.sh); здесь — проверка для status и doctor.
package iptables

import (
	"errors"
	"os/exec"
	"strings"
)

// Rule — аргументы правила таблицы nat, цепочки OUTPUT: новые соединения к плееру (9906), кроме соединений самого шима
// (они идут с адреса 127.0.0.2), заворачиваются на порт шима 19906. Тот же текст — в guard.sh.
const Rule = "-p tcp -d 127.0.0.1 --dport 9906 ! -s 127.0.0.2 -j REDIRECT --to-ports 19906"

// Command запускает iptables (подменяется в тестах).
var Command = func(args ...string) ([]byte, error) { return exec.Command("iptables", args...).CombinedOutput() }

// ErrMissing — iptables нет в системе.
var ErrMissing = errors.New("iptables не найден")

// Present — стоит ли правило шима.
func Present() (bool, error) {
	out, err := Command("-t", "nat", "-S", "OUTPUT")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, ErrMissing
		}
		return false, errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return strings.Contains(string(out), "--to-ports 19906"), nil
}

// Usable — работает ли таблица nat (нужна для режима shim): ядро и iptables колонки её поддерживают.
func Usable() error {
	out, err := Command("-t", "nat", "-S", "OUTPUT")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return ErrMissing
		}
		return errors.New("таблица nat недоступна: " + strings.TrimSpace(string(out)))
	}
	return nil
}
