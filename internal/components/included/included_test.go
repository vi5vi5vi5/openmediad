package included

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"openmediad/internal/components/catalog"
)

// Файлы include_<имя>.go соответствуют каталогу: по файлу на каждый готовый компонент, с его тегом и импортом его пакета,
// и никаких файлов для неготовых или неизвестных компонентов.
func TestIncludeFilesMatchCatalog(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), "include_")
		if !ok || !strings.HasSuffix(name, ".go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimSuffix(name, ".go")] = string(b)
	}
	build := regexp.MustCompile(`(?m)^//go:build (\S+)$`)
	imp := regexp.MustCompile(`(?m)^import _ "([^"]+)"$`)
	checked := 0
	for _, c := range catalog.All {
		key := strings.ReplaceAll(c.Name, "-", "_")
		src, ok := files[key]
		delete(files, key)
		if !c.Ready {
			if ok {
				t.Errorf("include_%s.go есть, а компонент в каталоге не готов", key)
			}
			continue
		}
		if !ok {
			t.Errorf("нет файла include_%s.go для готового компонента %s", key, c.Name)
			continue
		}
		if m := build.FindStringSubmatch(src); m == nil || m[1] != c.Tag() {
			t.Errorf("include_%s.go: строка //go:build должна быть %q", key, c.Tag())
		}
		if m := imp.FindStringSubmatch(src); m == nil || m[1] != "openmediad/internal/components/"+c.Name {
			t.Errorf("include_%s.go: должен импортироваться пакет openmediad/internal/components/%s", key, c.Name)
		}
		checked++
	}
	for key := range files {
		t.Errorf("include_%s.go: такого компонента нет в каталоге", key)
	}
	if checked == 0 {
		t.Fatal("ни одного файла не проверено")
	}
}
