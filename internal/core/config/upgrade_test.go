package config

import (
	"strings"
	"testing"
)

// Шаблон по умолчанию покрывает все ключи ядра: докладывать нечего (у тестового компонента в шаблоне нет двух ключей — это его особенность).
func TestTemplateCoversAllCoreKeys(t *testing.T) {
	_, added, err := Upgrade(Template())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range added {
		if a.Section != "debug-page" {
			t.Errorf("в шаблоне ядра нет ключа %s", a)
		}
	}
}

func TestUpgradeAddsMissingKeysWithTemplateComments(t *testing.T) {
	user := `# мои настройки
[general]
mode = "shim"      # мой комментарий
log_level = "warn"

[fallback]
max_crashes = 5

[route]
music = ["speaker"]
`
	out, added, err := Upgrade(user)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range added {
		got[a.String()] = true
	}
	for _, want := range []string{"general.log_entries", "general.log_file", "general.log_file_max_kb", "route.voice", "route.alert", "focus.duck_gain", "speaker.device", "speaker.aplay", "debug-page.enabled", "debug-page.delay", "debug-page.cors"} {
		if !got[want] {
			t.Errorf("не добавлено %s (добавлено: %v)", want, added)
		}
	}
	for _, kept := range []string{"general.mode", "general.log_level", "fallback.max_crashes", "route.music"} {
		if got[kept] {
			t.Errorf("тронут уже заданный ключ %s", kept)
		}
	}
	// значения и комментарии пользователя на месте
	for _, s := range []string{`mode = "shim"      # мой комментарий`, `log_level = "warn"`, "max_crashes = 5", "# мои настройки"} {
		if !strings.Contains(out, s) {
			t.Errorf("в результате пропало %q", s)
		}
	}
	// пояснения из шаблона приехали вместе с ключами
	if !strings.Contains(out, "log_entries = 2000") || !strings.Contains(out, "сколько записей журнала держать в памяти") {
		t.Errorf("в файл не попал комментарий шаблона:\n%s", out)
	}
	// продолжение пояснения (отступ + #) вставлено вместе с ключом
	if !strings.Contains(out, "log_file = \"\"") || !strings.Contains(out, "пусто — только в памяти (флеш не изнашивается)") {
		t.Errorf("многострочный комментарий log_file потерян:\n%s", out)
	}
	// новый ключ вставлен внутрь своей секции
	secGeneral := out[strings.Index(out, "[general]"):strings.Index(out, "[fallback]")]
	if !strings.Contains(secGeneral, "log_entries") || strings.Contains(secGeneral, "max_crashes") {
		t.Errorf("ключи вставлены не в ту секцию:\n%s", secGeneral)
	}
	// результат проходит проверку, значения прежние
	cfg, err := Load(out)
	if err != nil {
		t.Fatalf("результат не проходит проверку: %v", err)
	}
	if cfg.General.LogLevel != "warn" || cfg.Fallback.MaxCrashes != 5 {
		t.Fatalf("значения пользователя изменились: %+v %+v", cfg.General, cfg.Fallback)
	}
}

func TestUpgradeAppendsWholeMissingSection(t *testing.T) {
	out, added, err := Upgrade("[general]\nmode = \"shim\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[debug-page]") || !strings.Contains(out, "listen = \"0.0.0.0:6701\"  # адрес") {
		t.Fatalf("секция компонента не дописана целиком:\n%s", out)
	}
	if !strings.Contains(out, "[focus]") || !strings.Contains(out, "duck_gain") {
		t.Fatalf("секции ядра не дописаны:\n%s", out)
	}
	if len(added) < 10 {
		t.Fatalf("добавлено мало: %v", added)
	}
}

// Второй запуск ничего не меняет.
func TestUpgradeIsIdempotent(t *testing.T) {
	first, added, err := Upgrade("[general]\nlog_level = \"trace\"\n")
	if err != nil || len(added) == 0 {
		t.Fatalf("первый запуск: %v %v", added, err)
	}
	second, added2, err := Upgrade(first)
	if err != nil || len(added2) != 0 || second != first {
		t.Fatalf("второй запуск изменил файл: %v %v", added2, err)
	}
}

func TestUpgradeSynthesizesKeysMissingFromTemplate(t *testing.T) {
	// ключ delay есть в структуре тестового компонента, а в его шаблоне нет: строка строится из умолчаний
	out, added, err := Upgrade("[debug-page]\nenabled = true\nlisten = \"0.0.0.0:6701\"\n")
	if err != nil {
		t.Fatal(err)
	}
	var sawDelay bool
	for _, a := range added {
		sawDelay = sawDelay || a.String() == "debug-page.delay"
	}
	if !sawDelay || !strings.Contains(out, `delay = "300ms"`) {
		t.Fatalf("delay не дописан из умолчаний:\n%s", out)
	}
}

func TestUpgradeRefusesBrokenFile(t *testing.T) {
	for _, bad := range []string{"[general]\nmode = \"нет\"\n", "[general]\nlog_level = \n", "[неизвестная]\nx = 1\n"} {
		if _, _, err := Upgrade(bad); err == nil || !strings.Contains(err.Error(), "сначала исправьте") {
			t.Errorf("файл %q: %v", bad, err)
		}
	}
}

func TestTemplateParsingKeepsContinuationLines(t *testing.T) {
	d := parseTemplate("[a]\nk = 1   # раз\n            # два\nother = 2\n# отдельный комментарий\nz = 3\n")
	if got := d.keys["a"]["k"]; len(got) != 2 || !strings.Contains(got[1], "два") {
		t.Fatalf("блок k: %q", got)
	}
	if got := d.keys["a"]["other"]; len(got) != 1 {
		t.Fatalf("блок other захватил лишнее: %q", got)
	}
	if len(d.keys["a"]) != 3 {
		t.Fatalf("ключей %d, ждали 3", len(d.keys["a"]))
	}
}
