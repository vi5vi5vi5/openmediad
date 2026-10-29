package config

import (
	"strings"
	"testing"
	"time"

	_ "openmediad/internal/components/catalog" // каталог компонентов: ядро получает его от cmd/openmediad
	"openmediad/internal/core/registry"
)

// testDebug — настройки компонента для тестов (в этом тестовом бинарнике зарегистрирован только debug-page).
type testDebug struct {
	Toggle
	Listen string        `conf:"listen"`
	Delay  time.Duration `conf:"delay"`
	CORS   bool          `conf:"cors"`
}

func init() {
	registry.Register(registry.Component{
		Name: "debug-page",
		Defaults: func() any {
			return &testDebug{Toggle: Toggle{Enabled: true}, Listen: "0.0.0.0:6701", Delay: 300 * time.Millisecond}
		},
		Template: "[debug-page]\nenabled = true\nlisten = \"0.0.0.0:6701\"  # адрес\n",
	})
}

func TestTemplateLoadsAndEqualsDefaults(t *testing.T) {
	c, err := Load(Template())
	if err != nil {
		t.Fatalf("шаблон по умолчанию должен проходить проверку: %v", err)
	}
	d := Defaults()
	if c.General != d.General || c.Fallback != d.Fallback || c.Focus != d.Focus || c.Speaker != d.Speaker {
		t.Fatalf("шаблон расходится с умолчаниями:\n%+v\n%+v", c, d)
	}
	if !strings.Contains(Template(), "[debug-page]") {
		t.Fatal("в шаблоне нет секции компонента сборки")
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("у шаблона не должно быть замечаний: %v", c.Warnings)
	}
}

func TestParseValuesAndComments(t *testing.T) {
	doc, err := Parse(`# заголовок
[general]
mode = "shim"   # комментарий после значения
log_entries = 1_000
[route]
music = ["bt-out", "speaker"] # список
[debug-page]
listen = "a#b"  # решётка внутри строки — не комментарий
`)
	if err != nil {
		t.Fatal(err)
	}
	g := doc.Section("general")
	if v, _ := g.Get("mode"); v.Str != "shim" {
		t.Fatalf("mode = %+v", v)
	}
	if v, _ := g.Get("log_entries"); v.Num != 1000 {
		t.Fatalf("log_entries = %+v", v)
	}
	if v, _ := doc.Section("route").Get("music"); len(v.List) != 2 || v.List[0] != "bt-out" {
		t.Fatalf("music = %+v", v)
	}
	if v, _ := doc.Section("debug-page").Get("listen"); v.Str != "a#b" {
		t.Fatalf("listen = %+v", v)
	}
}

func TestErrorsHaveLineNumbers(t *testing.T) {
	cases := map[string]string{
		"[general]\nmode = shim\n":                   "строка 2",
		"[general]\nmode = \"shim\"\nmode = \"x\"\n": "строка 3: ключ \"mode\" повторяется",
		"[general]\nnope = 1\n":                      "строка 2: [general]: неизвестный ключ \"nope\"",
		"[general\n":                                 "строка 1",
		"[nope]\n":                                   "строка 1: неизвестная секция [nope]",
		"[general]\nlog_entries = \"много\"\n":       "строка 2: [general] log_entries: нужно целое число",
		"[debug-page]\ndelay = \"полсекунды\"\n":     "строка 2: [debug-page] delay: непонятная длительность",
		"[route]\nmusic = [\"a\" \"b\"]\n":           "строка 2",
		"mode = \"shim\"\n":                          "ключи до первой секции",
		"[general]\nmode = \"loud\"\n":               "[general] mode",
		"[focus]\nduck_gain = 2\n":                   "[focus] duck_gain",
		"[route]\nvoice = []\n":                      "нужен хотя бы один выход",
		"[route]\nvoice = [\"колонка\"]\n":           "неизвестный выход",
		"[general]\nlog_file = \"relative.log\"\n":   "нужен полный путь",
	}
	for text, want := range cases {
		_, err := Load(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ошибка %v, ждали с %q", text, err, want)
		}
	}
}

func TestUncompiledComponentAndSinkAreWarnings(t *testing.T) {
	c, err := Load("[spotify]\nenabled = true\n[route]\nmusic = [\"bt-out\", \"speaker\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 2 || !strings.Contains(c.Warnings[0], "spotify") || !strings.Contains(c.Warnings[1], "bt-out") {
		t.Fatalf("замечания: %v", c.Warnings)
	}
	if len(c.Route.Music) != 1 || c.Route.Music[0] != "speaker" {
		t.Fatalf("маршрут без невошедшего выхода: %v", c.Route.Music)
	}
}

func TestEnabled(t *testing.T) {
	c, err := Load("[debug-page]\nenabled = false\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled("debug-page") || c.Enabled("yandex-music") {
		t.Fatal("debug-page выключен в файле, yandex-music не вошёл в сборку")
	}
	if !Defaults().Enabled("debug-page") {
		t.Fatal("по умолчанию debug-page включён")
	}
}

func TestSetKeepsCommentsAndValidates(t *testing.T) {
	text := Template()
	out, err := Set(text, "general.log_level", "trace")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `log_level = "trace"       # trace | info | warn | error`) {
		t.Fatalf("строка ключа после правки:\n%s", line(out, "log_level"))
	}
	if strings.Count(out, "\n") != strings.Count(text, "\n") {
		t.Fatal("правка существующего ключа не должна менять число строк")
	}
	c, _ := Load(out)
	if c.General.LogLevel != "trace" {
		t.Fatal("значение не применилось")
	}
	if _, err := Set(text, "general.log_level", "громко"); err == nil {
		t.Fatal("недопустимое значение не должно записываться")
	}
	if _, err := Set(text, "general.nope", "1"); err == nil || !strings.Contains(err.Error(), "нет ключа") {
		t.Fatalf("неизвестный ключ: %v", err)
	}
	out, err = Set(text, "route.music", "bt-out, speaker") // выход не в сборке — замечание, а не ошибка
	if err != nil || !strings.Contains(line(out, "music"), `music = ["bt-out", "speaker"]`) {
		t.Fatalf("список из «a, b»: %v\n%s", err, line(out, "music"))
	}
	if _, err := Set(text, "route.music", "колонка"); err == nil {
		t.Fatal("неизвестный выход — ошибка")
	}
	out, err = Set(text, "debug-page.delay", "1s")
	if err != nil || !strings.Contains(out, "delay = \"1s\"") {
		t.Fatalf("новый ключ дописывается в секцию: %v\n%s", err, out)
	}
	if c, _ := Load(out); c.Components["debug-page"].(*testDebug).Delay != time.Second {
		t.Fatal("дописанный ключ не применился")
	}
	out, err = Set("", "focus.duck_gain", "0.5")
	if err != nil || !strings.Contains(out, "[focus]\nduck_gain = 0.5") {
		t.Fatalf("секция дописывается в пустой файл: %v %q", err, out)
	}
}

func TestGet(t *testing.T) {
	c := Defaults()
	for key, want := range map[string]string{
		"general.mode":       `"shim"`,
		"route.voice":        `["speaker"]`,
		"focus.duck_gain":    "0.25",
		"debug-page.enabled": "true",
		"debug-page.delay":   `"300ms"`,
	} {
		if got, err := Get(c, key); err != nil || got != want {
			t.Errorf("%s = %q (%v), ждали %q", key, got, err, want)
		}
	}
	for _, bad := range []string{"mode", "general.", "nope.key", "spotify.enabled"} {
		if _, err := Get(c, bad); err == nil {
			t.Errorf("%s: ждали ошибку", bad)
		}
	}
}

func line(text, key string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), key) {
			return l
		}
	}
	return ""
}
