package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runCLI(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(&Ctx{Stdin: strings.NewReader(""), Stdout: &o, Stderr: &e}, args)
	return code, o.String(), e.String()
}

func TestNoArgsPrintsHelpAndExitsZero(t *testing.T) {
	code, out, _ := runCLI()
	if code != ExitOK {
		t.Fatalf("код %d, ждали 0", code)
	}
	for _, want := range []string{"openmediad ", "сборка:", "компоненты:", "использование: openmediad <команда>", "version"} {
		if !strings.Contains(out, want) {
			t.Errorf("в помощи нет %q:\n%s", want, out)
		}
	}
}

func TestHelpAliases(t *testing.T) {
	for _, a := range []string{"-h", "--help", "help"} {
		if code, out, _ := runCLI(a); code != ExitOK || !strings.Contains(out, "использование") {
			t.Errorf("%s: код %d, вывод %q", a, code, out)
		}
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, _, errOut := runCLI("нет-такой")
	if code != ExitUsage || !strings.Contains(errOut, "неизвестная команда") {
		t.Fatalf("код %d, stderr %q", code, errOut)
	}
}

func TestVersionIsOneLine(t *testing.T) {
	for _, a := range []string{"version", "--version"} {
		code, out, _ := runCLI(a)
		if code != ExitOK || strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "openmediad ") {
			t.Errorf("%s: код %d, вывод %q", a, code, out)
		}
	}
}

func TestExtraArgsAreUsageError(t *testing.T) {
	if code, _, _ := runCLI("version", "лишнее"); code != ExitUsage {
		t.Fatalf("код %d, ждали %d", code, ExitUsage)
	}
}
