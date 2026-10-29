package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"openmediad/internal/components/catalog"
)

const modulePrefix = "openmediad/internal/"

// goFiles возвращает пути .go-файлов под internal/ (без тестов), сгруппированные по папкам относительно internal/.
func goFiles(t *testing.T) map[string][]string {
	t.Helper()
	root := ".."
	out := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		out[filepath.ToSlash(rel)] = append(out[filepath.ToSlash(rel)], path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func imports(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if rest, ok := strings.CutPrefix(p, modulePrefix); ok {
			out = append(out, rest)
		}
	}
	return out
}

func zone(path string) string { z, _, _ := strings.Cut(path, "/"); return z }

// componentOf — имя компонента, которому принадлежит папка internal/components/<имя>/…; "" — это catalog и included.
func componentOf(dir string) string {
	rest, ok := strings.CutPrefix(dir, "components/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "catalog" || name == "included" {
		return ""
	}
	return name
}

// Правила зависимостей:
//   - core импортирует только core;
//   - libs — core и libs;
//   - компонент — core, libs, свои подпакеты и компоненты, перечисленные в его паспорте (NeedsComponents);
//   - catalog — только core/registry; included — только компоненты;
//   - devtools и cmd — что угодно.
//
// Тесты в правила не входят.
func TestDependencyRules(t *testing.T) {
	for dir, files := range goFiles(t) {
		z := zone(dir)
		comp := componentOf(dir)
		var allowedComponents []string
		if info, ok := catalog.Lookup(comp); ok {
			allowedComponents = info.NeedsComponents
		}
		for _, file := range files {
			for _, imp := range imports(t, file) {
				iz := zone(imp)
				bad := ""
				switch {
				case z == "core" && iz != "core":
					bad = "ядро (core) может импортировать только ядро"
				case z == "libs" && iz != "core" && iz != "libs":
					bad = "библиотеки (libs) могут импортировать только core и libs"
				case z == "components" && comp != "":
					switch iz {
					case "core", "libs":
					case "components":
						other := componentOf(imp)
						if other != comp && !slices.Contains(allowedComponents, other) {
							bad = "компонент не может импортировать другой компонент, если тот не указан в его паспорте (NeedsComponents)"
						}
					default:
						bad = "компонент может импортировать только core, libs и своё"
					}
				case dir == "components/catalog" && imp != "core/registry":
					bad = "каталог описывает компоненты и не должен импортировать ничего, кроме core/registry"
				case dir == "components/included" && iz != "components":
					bad = "included подключает только компоненты"
				}
				if bad != "" {
					t.Errorf("%s импортирует %s: %s", file, imp, bad)
				}
			}
		}
	}
}

// В коде ядра нет ни одного имени компонента: ни как строки, ни как идентификатора директивы. Ядро получает всё нужное от
// компонентов через точки расширения (StatusLine, OnDirective, command.Register, command.RegisterTestOutput). Комментарии не
// проверяются; проверяются литералы, равные имени компонента из каталога или его прежнему имени.
func TestCoreKnowsNoComponentByName(t *testing.T) {
	banned := map[string]bool{"bluetooth": true, "btin": true, "yandex": true, "tts": true, "sounds": true, "pprof": true}
	for _, c := range catalog.All {
		banned[c.Name] = true
	}
	for dir, files := range goFiles(t) {
		if zone(dir) != "core" {
			continue
		}
		for _, file := range files {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && banned[s] {
						t.Errorf("%s: в коде ядра имя компонента %q", file, s)
					}
				}
				return true
			})
		}
	}
}

// Каждая папка с кодом в internal/ описана в README.md (зачем, что внутри, от чего зависит).
func TestEveryFolderHasReadme(t *testing.T) {
	for dir := range goFiles(t) {
		if dir == "." || dir == "architecture" {
			continue
		}
		if _, err := os.Stat(filepath.Join("..", filepath.FromSlash(dir), "README.md")); err != nil {
			t.Errorf("internal/%s: нет README.md", dir)
		}
	}
}
