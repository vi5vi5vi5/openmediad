package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"openmediad/internal/registry"
)

// Файлы with_<имя>.go должны соответствовать каталогу: по файлу на каждый готовый компонент, с его тегом сборки,
// и никаких файлов для неготовых или неизвестных компонентов.
func TestWithFilesMatchCatalog(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), "with_")
		if !ok || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimSuffix(name, ".go")] = string(b)
	}
	build := regexp.MustCompile(`(?m)^//go:build (\S+)$`)
	checked := 0
	for _, c := range registry.Catalog {
		src, ok := files[c.Name]
		delete(files, c.Name)
		if !c.Ready {
			if ok {
				t.Errorf("with_%s.go есть, а компонент в каталоге не готов (Ready: false)", c.Name)
			}
			continue
		}
		if !ok {
			t.Errorf("нет файла with_%s.go для готового компонента", c.Name)
			continue
		}
		want := c.Tag()
		if c.Default {
			want = "!" + want
		}
		m := build.FindStringSubmatch(src)
		if m == nil || m[1] != want {
			t.Errorf("with_%s.go: строка //go:build должна быть %q", c.Name, want)
		}
		checked++
	}
	for name := range files {
		t.Errorf("with_%s.go: компонента %q нет в каталоге", name, name)
	}
	if checked == 0 {
		t.Fatal("ни одного файла не проверено")
	}
}

// В сборке по умолчанию зарегистрированы ровно компоненты «по умолчанию» (тест идёт без тегов).
func TestDefaultBuildRegistersDefaults(t *testing.T) {
	for _, c := range registry.Catalog {
		_, got := registry.Get(c.Name)
		if want := c.Ready && c.Default; got != want {
			t.Errorf("%s: зарегистрирован %v, ждали %v", c.Name, got, want)
		}
	}
}
