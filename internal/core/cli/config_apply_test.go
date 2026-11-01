package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func confFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openmediad.conf")
	if text != "" {
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENMEDIAD_CONFIG", p)
	t.Setenv("OPENMEDIAD_RUN", t.TempDir()) // сокета демона нет
	return p
}

const partialConf = "[general]\nlog_level = \"warn\"   # мой уровень\n"

func TestConfigUpgradeDryRunChangesNothing(t *testing.T) {
	p := confFile(t, partialConf)
	code, out, errOut := runCLI("config", "upgrade", "-n")
	if code != ExitOK {
		t.Fatalf("код %d: %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "+ general.log_entries") || !strings.Contains(out, "+ focus.duck_gain") || !strings.Contains(out, "пробный прогон") {
		t.Fatalf("вывод пробного прогона:\n%s", out)
	}
	if b, _ := os.ReadFile(p); string(b) != partialConf {
		t.Fatal("пробный прогон изменил файл")
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("пробный прогон создал .bak")
	}
}

func TestConfigUpgradeWritesBackupAndKeepsUserValues(t *testing.T) {
	p := confFile(t, partialConf)
	code, out, errOut := runCLI("config", "upgrade")
	if code != ExitOK {
		t.Fatalf("код %d: %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "config update") {
		t.Fatalf("не подсказано, как применить:\n%s", out)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, s := range []string{`log_level = "warn"   # мой уровень`, "log_entries = 2000", "[focus]", "duck_gain"} {
		if !strings.Contains(got, s) {
			t.Errorf("в файле нет %q:\n%s", s, got)
		}
	}
	if bak, _ := os.ReadFile(p + ".bak"); string(bak) != partialConf {
		t.Fatalf(".bak не совпадает со старым файлом: %q", bak)
	}
	// второй запуск ничего не находит
	code, out, _ = runCLI("config", "upgrade")
	if code != ExitOK || !strings.Contains(out, "есть все ключи") {
		t.Fatalf("повторный запуск: %d %q", code, out)
	}
}

func TestConfigUpgradeWithoutFileAndWithBrokenFile(t *testing.T) {
	confFile(t, "")
	if code, out, _ := runCLI("config", "upgrade"); code != ExitOK || !strings.Contains(out, "файла нет") {
		t.Fatalf("без файла: %d %q", code, out)
	}
	p := confFile(t, "[general]\nmode = \"нет\"\n")
	code, _, errOut := runCLI("config", "upgrade")
	if code == ExitOK || !strings.Contains(errOut, "сначала исправьте") {
		t.Fatalf("битый файл: %d %q", code, errOut)
	}
	if b, _ := os.ReadFile(p); string(b) != "[general]\nmode = \"нет\"\n" {
		t.Fatal("битый файл изменён")
	}
}

func TestConfigUpdateValidatesBeforeHandingToDaemon(t *testing.T) {
	confFile(t, "[general]\nlog_level = \"шумно\"\n")
	code, _, errOut := runCLI("config", "update")
	if code == ExitOK || !strings.Contains(errOut, "демону настройки не отданы") {
		t.Fatalf("битый файл: %d %q", code, errOut)
	}
	confFile(t, "[general]\nlog_level = \"warn\"\n")
	code, out, errOut := runCLI("config", "update")
	if code != ExitOK || !strings.Contains(out, "демон не запущен") {
		t.Fatalf("исправный файл без демона: %d %q %q", code, out, errOut)
	}
	if code, _, _ := runCLI("config", "update", "лишнее"); code != ExitUsage {
		t.Fatalf("лишний аргумент: код %d", code)
	}
}
