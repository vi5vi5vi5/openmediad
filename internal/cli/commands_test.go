package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"encoding/binary"

	"openmediad/internal/audio"
)

// sandbox — настройки и сокет во временном каталоге (демон «не запущен»).
func sandbox(t *testing.T) (conf string) {
	t.Helper()
	dir := t.TempDir()
	conf = filepath.Join(dir, "openmediad.conf")
	t.Setenv("OPENMEDIAD_CONFIG", conf)
	t.Setenv("OPENMEDIAD_RUN", filepath.Join(dir, "run"))
	return conf
}

func TestConfigSetGetCheck(t *testing.T) {
	conf := sandbox(t)
	if code, out, _ := runCLI("config", "path"); code != 0 || strings.TrimSpace(out) != conf {
		t.Fatalf("path: %d %q", code, out)
	}
	if code, out, _ := runCLI("config", "check"); code != 0 || !strings.Contains(out, "файла нет") {
		t.Fatalf("check без файла: %d %q", code, out)
	}
	if code, out, _ := runCLI("config", "get", "general.log_level"); code != 0 || strings.TrimSpace(out) != `"info"` {
		t.Fatalf("get: %d %q", code, out)
	}
	code, out, errOut := runCLI("config", "set", "general.log_level", "trace")
	if code != 0 || !strings.Contains(out, `general.log_level = "trace"`) || !strings.Contains(out, "демон не запущен") {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	b, _ := os.ReadFile(conf)
	if !strings.Contains(string(b), `log_level = "trace"`) || !strings.Contains(string(b), "# trace | info | warn | error") {
		t.Fatalf("файл после set (комментарии должны сохраниться):\n%s", b)
	}
	if code, out, _ := runCLI("config", "get", "general.log_level"); code != 0 || strings.TrimSpace(out) != `"trace"` {
		t.Fatalf("get после set: %d %q", code, out)
	}
	if code, _, errOut := runCLI("config", "set", "general.log_level", "громко"); code != ExitError || !strings.Contains(errOut, "log_level") {
		t.Fatalf("set неверного значения: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("config", "set", "general.nope", "1"); code != ExitError || !strings.Contains(errOut, "нет ключа") {
		t.Fatalf("set неизвестного ключа: %d %q", code, errOut)
	}
	if code, out, _ := runCLI("config", "check"); code != 0 || !strings.Contains(out, "верны") {
		t.Fatalf("check: %d %q", code, out)
	}
	os.WriteFile(conf, []byte("[general]\nlog_level = trace\n"), 0o644)
	if code, _, errOut := runCLI("config", "check"); code != ExitError || !strings.Contains(errOut, "строка 2") {
		t.Fatalf("check неверного файла: %d %q", code, errOut)
	}
	if code, _, _ := runCLI("config", "reset", "-yes"); code != 0 {
		t.Fatalf("reset: %d", code)
	}
	if bak, err := os.ReadFile(conf + ".bak"); err != nil || !strings.Contains(string(bak), "log_level = trace") {
		t.Fatalf("reset не сохранил старый файл: %v", err)
	}
	if code, out, _ := runCLI("config", "show"); code != 0 || !strings.Contains(out, "[general]") {
		t.Fatalf("show: %d", code)
	}
	if code, _, _ := runCLI("config", "чтото"); code != ExitUsage {
		t.Fatalf("неизвестное действие: код %d", code)
	}
}

func TestConfigResetAsks(t *testing.T) {
	conf := sandbox(t)
	os.WriteFile(conf, []byte("[general]\n"), 0o644)
	// stdin пуст (не терминал): ответ «нет»
	if code, _, errOut := runCLI("config", "reset"); code != ExitError || !strings.Contains(errOut, "отменено") {
		t.Fatalf("reset без подтверждения: %d %q", code, errOut)
	}
}

func TestStatusAndLogsWithoutDaemon(t *testing.T) {
	sandbox(t)
	code, out, errOut := runCLI("status")
	if code != ExitError || !strings.Contains(out, "не запущен") || errOut != "" {
		t.Fatalf("status: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := runCLI("logs"); code != ExitError || !strings.Contains(errOut, "не запущен") {
		t.Fatalf("logs: %d %q", code, errOut)
	}
	if code, _, _ := runCLI("logs", "-level", "громко"); code != ExitUsage {
		t.Fatalf("logs -level: код %d", code)
	}
}

func TestComponentsTable(t *testing.T) {
	sandbox(t)
	code, out, _ := runCLI("components", "-all")
	if code != 0 {
		t.Fatalf("код %d", code)
	}
	// в тестах cli компоненты не импортированы: все готовые — «не в сборке», планы — с фазой
	for _, want := range []string{"yandex", "bluetooth", "фаза 7", "демон не запущен"} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в выводе:\n%s", want, out)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for _, c := range []struct {
		s    int
		want string
	}{{5, "5 с"}, {125, "2 мин"}, {3*3600 + 300, "3 ч 5 мин"}, {50 * 3600, "2 дн 2 ч"}} {
		if got := humanDuration(secs(c.s)); got != c.want {
			t.Errorf("%d с → %q, ждали %q", c.s, got, c.want)
		}
	}
}

func secs(s int) time.Duration { return time.Duration(s) * time.Second }

func TestSystemCommandsRefuseOffDevice(t *testing.T) {
	sandbox(t)
	for _, cmd := range []string{"start", "stop", "restart"} {
		code, _, errOut := runCLI(cmd)
		if code != ExitNotHere || !strings.Contains(errOut, "только на колонке") {
			t.Errorf("%s на компьютере: код %d, %q", cmd, code, errOut)
		}
	}
}

func TestMoreSystemCommandsRefuseOffDevice(t *testing.T) {
	sandbox(t)
	for _, cmd := range []string{"install", "uninstall", "enable", "disable", "doctor", "reboot"} {
		if code, _, errOut := runCLI(cmd, "-yes"); cmd == "doctor" || cmd == "enable" || cmd == "disable" {
			if code2, _, errOut2 := runCLI(cmd); code2 != ExitNotHere {
				t.Errorf("%s на компьютере: код %d, %q", cmd, code2, errOut2)
			}
		} else if code != ExitNotHere {
			t.Errorf("%s на компьютере: код %d, %q", cmd, code, errOut)
		}
	}
}

func TestToneHasSoftEdges(t *testing.T) {
	f := audio.Stereo48
	b := tone(f, 440, 100*time.Millisecond, audio.DB(-30))
	if len(b) != f.Bytes(100*time.Millisecond) {
		t.Fatalf("длина %d", len(b))
	}
	first := int16(binary.LittleEndian.Uint16(b[0:]))
	last := int16(binary.LittleEndian.Uint16(b[len(b)-2:]))
	if first != 0 || last > 50 || last < -50 {
		t.Errorf("края не плавные: %d … %d", first, last)
	}
}

func TestBluetoothLine(t *testing.T) {
	base := map[string]any{"bus": true, "connected": true, "name": "ROG", "device": "A0:AD", "volume_127": float64(63)}
	if got := bluetoothLine(base); got != "ROG (A0:AD) подключены, громкость 63/127 за колонкой; канал в наушники не открыт" {
		t.Fatalf("без канала: %q", got)
	}
	base["channel"], base["streams"] = true, float64(2)
	if got := bluetoothLine(base); !strings.HasSuffix(got, "; канал открыт, потоков 2") {
		t.Fatalf("с каналом: %q", got)
	}
	base["buttons"] = true
	if got := bluetoothLine(base); !strings.HasSuffix(got, "; кнопки наушников работают") {
		t.Fatalf("с кнопками: %q", got)
	}
}
