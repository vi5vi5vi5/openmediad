package cli

import (
	"bufio"
	"strings"
)

// ask задаёт вопрос «да/нет» и читает ответ из stdin. Пустой ответ — «да»; конец ввода (не терминал) — «нет».
func ask(c *Ctx, question string) bool {
	c.Printf("%s [Д/н] ", question)
	line, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && line == "" {
		c.Printf("\n")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "д", "да", "y", "yes":
		return true
	}
	return false
}
