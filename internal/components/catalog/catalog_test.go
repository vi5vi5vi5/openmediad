package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"

	// Все готовые компоненты: в этом тестовом бинарнике они зарегистрированы, как в сборке профиля full.
	_ "openmediad/internal/components/alice-voice"
	_ "openmediad/internal/components/bt-in"
	_ "openmediad/internal/components/bt-out"
	_ "openmediad/internal/components/debug-page"
	_ "openmediad/internal/components/debug-profiler"
	_ "openmediad/internal/components/display-spectrum"
	_ "openmediad/internal/components/system-sounds"
	_ "openmediad/internal/components/yandex-music"

	"openmediad/internal/core/config"
	"openmediad/internal/core/registry"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Паспорта самосогласованы: имена, секции, теги не повторяются, зависимости существуют и готовы.
func TestCatalogConsistency(t *testing.T) {
	names, tags, sections := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range All {
		if !nameRE.MatchString(c.Name) {
			t.Errorf("%q: имя из строчных латинских букв, цифр и «-»", c.Name)
		}
		if names[c.Name] || tags[c.Tag()] {
			t.Errorf("%s: имя или тег повторяются", c.Name)
		}
		names[c.Name], tags[c.Tag()] = true, true
		if c.Section != "" {
			if c.Section != c.Name {
				t.Errorf("%s: секция настроек должна называться так же, как компонент (сейчас %q)", c.Name, c.Section)
			}
			if sections[c.Section] {
				t.Errorf("%s: секция %q повторяется", c.Name, c.Section)
			}
			sections[c.Section] = true
		}
		if c.Title == "" || c.Summary == "" {
			t.Errorf("%s: нет названия или описания", c.Name)
		}
		if c.Ready {
			if c.WithoutIt == "" {
				t.Errorf("%s: готовый компонент без строки «если убрать»", c.Name)
			}
			for _, n := range c.Needs {
				if n.ID == "" || n.Text == "" {
					t.Errorf("%s: пустая запись «нужно от прошивки»", c.Name)
				}
			}
		} else {
			if c.Phase == 0 {
				t.Errorf("%s: не готов, но фаза плана не указана", c.Name)
			}
			if c.Default {
				t.Errorf("%s: нереализованный компонент не может быть в сборке по умолчанию", c.Name)
			}
		}
	}
	for _, c := range All {
		for _, dep := range c.NeedsComponents {
			d, ok := Lookup(dep)
			switch {
			case !ok:
				t.Errorf("%s: зависимость %q — нет такого компонента", c.Name, dep)
			case !d.Ready:
				t.Errorf("%s: зависимость %q не реализована", c.Name, dep)
			case c.Default && !d.Default:
				t.Errorf("%s входит в сборку по умолчанию, а его зависимость %s — нет", c.Name, dep)
			}
		}
	}
}

func TestTags(t *testing.T) {
	c := registry.Info{Name: "bt-out", Ready: true}
	if c.Tag() != "omd_bt_out" || !c.InBuild([]string{"omd_bt_out"}) || c.InBuild([]string{"omd_bt_in"}) || c.InBuild(nil) {
		t.Fatal("тег компонента — omd_ и имя с «_»; без тега в сборку не входит")
	}
	if (registry.Info{Name: "x"}).InBuild([]string{"omd_x"}) {
		t.Fatal("нереализованный компонент в сборку не входит")
	}
}

func TestProfiles(t *testing.T) {
	if core, _ := Profile(ProfileCore); len(core) != 0 {
		t.Fatalf("core — только ядро: %v", core)
	}
	std, _ := Profile(ProfileStandard)
	full, _ := Profile(ProfileFull)
	for _, c := range All {
		if got, want := slices.Contains(std, c.Name), c.Ready && c.Default; got != want {
			t.Errorf("standard: %s = %v, ждали %v", c.Name, got, want)
		}
		if got, want := slices.Contains(full, c.Name), c.Ready; got != want {
			t.Errorf("full: %s = %v, ждали %v", c.Name, got, want)
		}
	}
	if _, err := Profile("нет"); err == nil {
		t.Fatal("неизвестный профиль — ошибка")
	}
}

// Зарегистрировались ровно готовые компоненты, и все они есть в каталоге.
func TestRegisteredComponentsMatchCatalog(t *testing.T) {
	if err := registry.Verify(); err != nil {
		t.Fatal(err)
	}
	for _, c := range All {
		if _, got := registry.Get(c.Name); got != c.Ready {
			t.Errorf("%s: зарегистрирован %v, готов %v", c.Name, got, c.Ready)
		}
	}
}

// Файл настроек по умолчанию (его пишут install и config reset) собирается из шаблонов ядра и компонентов сборки: он обязан
// разбираться и давать ровно умолчания. Шаблоны компонентов проверяются здесь — в пакете config компонентов нет.
func TestTemplateOfAllComponentsLoadsToDefaults(t *testing.T) {
	c, err := config.Load(config.Template())
	if err != nil {
		t.Fatalf("шаблон настроек не разбирается: %v", err)
	}
	if len(c.Warnings) > 0 {
		t.Fatalf("предупреждения в шаблоне: %v", c.Warnings)
	}
	def := config.Defaults()
	for name, v := range def.Components {
		if !reflect.DeepEqual(c.Components[name], v) {
			t.Errorf("[%s]: в шаблоне %+v, умолчания %+v", name, c.Components[name], v)
		}
	}
	for _, info := range All {
		if _, ok := registry.Get(info.Name); ok && info.Section != "" {
			if _, ok := def.Components[info.Name]; !ok {
				t.Errorf("%s: в каталоге есть секция, а настроек нет", info.Name)
			}
		}
	}
}

// docs/COMPONENTS.md и паспорта в README.md компонентов совпадают с каталогом. Не совпадают — go run ./tools/build -sync-docs.
func TestDocsInSync(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "COMPONENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != ComponentsDoc() {
		t.Error("docs/COMPONENTS.md устарел: go run ./tools/build -sync-docs")
	}
	for _, c := range All {
		if !c.Ready {
			continue
		}
		path := filepath.Join("..", c.Name, "README.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		fresh, ok := ReplacePassport(string(b), c)
		switch {
		case !ok:
			t.Errorf("%s: нет маркеров паспорта", path)
		case fresh != string(b):
			t.Errorf("%s: паспорт устарел: go run ./tools/build -sync-docs", path)
		}
	}
}
