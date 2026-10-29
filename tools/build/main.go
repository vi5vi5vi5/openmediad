// build — сборка openmediad с выбором компонентов. Работает на Windows, Linux и macOS: make и sh не нужны.
//
//	go run ./tools/build                           под колонку (linux/arm64), профиль standard → dist/openmediad
//	go run ./tools/build -profile core             только ядро (прозрачный посредник); профили: core, standard, full
//	go run ./tools/build -with bt-out              добавить компонент к профилю
//	go run ./tools/build -without debug-page       убрать компонент из профиля
//	go run ./tools/build -host                     под этот компьютер (для проверок)
//	go run ./tools/build -list                     компоненты с паспортами
//	go run ./tools/build -check                    собрать профили core, standard, full и инструменты разработчика
//	go run ./tools/build -dev                      собрать openmediad-dev (инструменты разработчика)
//	go run ./tools/build -deploy 192.168.1.12      собрать и залить на колонку по ssh (порт 2222) в /tmp/openmediad/
//	go run ./tools/build -verify                   целостность vendor/: хэши файлов и go mod verify
//	go run ./tools/build -sync-docs                переписать docs/COMPONENTS.md и паспорта в README.md компонентов из каталога
//
// Теги сборки вручную писать не нужно: инструмент собирает их из профиля и флагов. Тег компонента — omd_ и его имя с «_»
// вместо «-» (omd_yandex_music); подробности — internal/components/catalog.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"openmediad/internal/components/catalog"
	"openmediad/internal/core/registry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}

type opts struct {
	profile       string
	with, without []string
	host, dev     bool
	out           string
}

func run() error {
	profile := flag.String("profile", catalog.ProfileStandard, "профиль сборки: core | standard | full")
	with := flag.String("with", "", "добавить компоненты к профилю (через запятую)")
	without := flag.String("without", "", "убрать компоненты из профиля (через запятую)")
	host := flag.Bool("host", false, "собрать под этот компьютер, а не под колонку")
	list := flag.Bool("list", false, "показать компоненты с паспортами")
	check := flag.Bool("check", false, "собрать профили core, standard, full и инструменты разработчика (проверка, что всё собирается)")
	dev := flag.Bool("dev", false, "собрать openmediad-dev вместо openmediad")
	deploy := flag.String("deploy", "", "после сборки залить на колонку: адрес[:ssh-порт] (по умолчанию порт 2222)")
	verify := flag.Bool("verify", false, "проверить целостность vendor/")
	syncDocs := flag.Bool("sync-docs", false, "переписать docs/COMPONENTS.md и паспорта в README.md компонентов из каталога")
	out := flag.String("o", "", "куда положить бинарник (по умолчанию dist/openmediad или dist/openmediad-dev)")
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("лишние аргументы: %s", strings.Join(flag.Args(), " "))
	}
	if err := chdirRoot(); err != nil {
		return err
	}
	switch {
	case *list:
		printList()
		return nil
	case *verify:
		return verifyVendor()
	case *syncDocs:
		return syncAllDocs()
	case *check:
		return checkAll()
	}
	o := opts{profile: *profile, with: split(*with), without: split(*without), host: *host, dev: *dev, out: *out}
	bin, err := build(o)
	if err != nil {
		return err
	}
	if *deploy != "" {
		if o.host {
			return errors.New("-deploy и -host вместе не имеют смысла: на колонку нужна сборка linux/arm64")
		}
		return deployTo(bin, *deploy)
	}
	return nil
}

// chdirRoot переходит в корень модуля, чтобы инструмент работал из любого каталога проекта.
func chdirRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "openmediad")); err == nil {
				return os.Chdir(dir)
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			return errors.New("не найден корень проекта openmediad (go.mod и cmd/openmediad)")
		}
		dir = up
	}
}

func split(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// component ищет готовый компонент каталога по имени.
func component(name string) (registry.Info, error) {
	c, ok := catalog.Lookup(name)
	if !ok {
		return c, fmt.Errorf("нет компонента %q (список: go run ./tools/build -list)", name)
	}
	if !c.Ready {
		return c, fmt.Errorf("компонент %q ещё не реализован (фаза %d плана)", name, c.Phase)
	}
	return c, nil
}

// resolve собирает набор компонентов: профиль, плюс -with, минус -without, плюс компоненты, без которых выбранные не
// работают (debug-profiler требует debug-page). notes — что инструмент добавил сам.
func resolve(profile string, with, without []string) (names, notes []string, err error) {
	base, err := catalog.Profile(profile)
	if err != nil {
		return nil, nil, err
	}
	set := map[string]bool{}
	for _, n := range base {
		set[n] = true
	}
	for _, n := range with {
		if _, err := component(n); err != nil {
			return nil, nil, err
		}
		set[n] = true
	}
	removed := map[string]bool{}
	for _, n := range without {
		if _, err := component(n); err != nil {
			return nil, nil, err
		}
		delete(set, n)
		removed[n] = true
	}
	for changed := true; changed; {
		changed = false
		for _, c := range catalog.All {
			if !set[c.Name] {
				continue
			}
			for _, dep := range c.NeedsComponents {
				switch {
				case set[dep]:
				case removed[dep]:
					return nil, nil, fmt.Errorf("%s требует %s, а он убран (-without)", c.Name, dep)
				default:
					set[dep] = true
					changed = true
					notes = append(notes, fmt.Sprintf("добавлен %s: он нужен компоненту %s", dep, c.Name))
				}
			}
		}
	}
	for _, c := range catalog.All {
		if set[c.Name] {
			names = append(names, c.Name)
		}
	}
	return names, notes, nil
}

// tagsOf — теги сборки для набора компонентов.
func tagsOf(names []string) []string {
	var tags []string
	for _, n := range names {
		c, _ := catalog.Lookup(n)
		tags = append(tags, c.Tag())
	}
	return tags
}

func printList() {
	fmt.Printf("%-17s %-16s %-12s %-12s %-22s %s\n", "компонент", "класс", "по умолчанию", "зрелость", "тег", "нужно от компонентов · что делает")
	for _, c := range catalog.All {
		def, mat := "нет", "проверен"
		if c.Default {
			def = "да"
		}
		if c.Experimental {
			mat = "опытный"
		}
		if !c.Ready {
			def, mat = "—", fmt.Sprintf("фаза %d", c.Phase)
		}
		needs := ""
		if len(c.NeedsComponents) > 0 {
			needs = "[" + strings.Join(c.NeedsComponents, ", ") + "] "
		}
		fmt.Printf("%-17s %-16s %-12s %-12s %-22s %s%s\n", c.Name, c.Class, def, mat, c.Tag(), needs, c.Summary)
	}
	fmt.Println("\nпрофили: core — только ядро; standard — компоненты «по умолчанию» (профиль сборки по умолчанию); full — все готовые")
}

// build собирает бинарник и возвращает путь к нему.
func build(o opts) (string, error) {
	var names, notes, tags = []string(nil), []string(nil), []string(nil)
	name := "openmediad"
	if o.dev {
		name = "openmediad-dev" // инструментам компоненты по тегам не нужны: они подключают нужные пакеты прямо
	} else {
		var err error
		if names, notes, err = resolve(o.profile, o.with, o.without); err != nil {
			return "", err
		}
		tags = tagsOf(names)
	}
	for _, n := range notes {
		fmt.Println("внимание:", n)
	}
	goos, goarch := "linux", "arm64"
	if o.host {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	out := o.out
	if out == "" {
		out = filepath.Join("dist", name)
		if goos == "windows" {
			out += ".exe"
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := goBuild(goos, goarch, tags, "./cmd/"+name, out); err != nil {
		return "", err
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", err
	}
	what := "инструменты разработчика"
	if !o.dev {
		what = "компоненты: " + listOrCore(names)
	}
	fmt.Printf("собрано: %s (%s/%s, %.2f МиБ; %s; теги: %s)\n", out, goos, goarch, float64(st.Size())/(1<<20), what, tagList(tags))
	return out, nil
}

func listOrCore(names []string) string {
	if len(names) == 0 {
		return "нет (только ядро)"
	}
	return strings.Join(names, " ")
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "нет"
	}
	return strings.Join(tags, ",")
}

func goBuild(goos, goarch string, tags []string, pkg, out string) error {
	ldflags := fmt.Sprintf("-s -w -X openmediad/internal/core/buildinfo.Version=%s -X openmediad/internal/core/buildinfo.Date=%s",
		version(), time.Now().Format("2006-01-02"))
	args := []string{"build", "-trimpath", "-ldflags", ldflags}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "-o", out, pkg)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s: %w", pkg, err)
	}
	return nil
}

// version — тег git, если он стоит ровно на текущем коммите, иначе содержимое файла VERSION.
func version() string {
	if b, err := exec.Command("git", "describe", "--tags", "--exact-match").Output(); err == nil {
		if v := strings.TrimPrefix(strings.TrimSpace(string(b)), "v"); v != "" {
			return v
		}
	}
	if b, err := os.ReadFile("VERSION"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return "0.0.0-dev"
}

// checkAll собирает под колонку профили core, standard, full и инструменты разработчика.
func checkAll() error {
	tmp, err := os.MkdirTemp("", "openmediad-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	sets := []struct {
		label string
		o     opts
	}{
		{"core — только ядро", opts{profile: catalog.ProfileCore}},
		{"standard — по умолчанию", opts{profile: catalog.ProfileStandard}},
		{"full — все готовые компоненты", opts{profile: catalog.ProfileFull}},
		{"инструменты разработчика", opts{dev: true}},
	}
	for i, s := range sets {
		fmt.Printf("== %s\n", s.label)
		s.o.out = filepath.Join(tmp, fmt.Sprintf("bin%d", i))
		if _, err := build(s.o); err != nil {
			return fmt.Errorf("набор «%s»: %w", s.label, err)
		}
	}
	fmt.Println("все наборы собираются")
	return nil
}

// syncAllDocs переписывает docs/COMPONENTS.md и блоки «Паспорт» в README.md компонентов.
func syncAllDocs() error {
	if err := os.WriteFile(filepath.Join("docs", "COMPONENTS.md"), []byte(catalog.ComponentsDoc()), 0o644); err != nil {
		return err
	}
	fmt.Println("docs/COMPONENTS.md")
	for _, c := range catalog.All {
		if !c.Ready {
			continue
		}
		path := filepath.Join("internal", "components", c.Name, "README.md")
		old, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w (создайте README.md с маркерами паспорта)", path, err)
		}
		text, ok := catalog.ReplacePassport(string(old), c)
		if !ok {
			return fmt.Errorf("%s: нет маркеров паспорта (%s … %s)", path, catalog.PassportBegin, catalog.PassportEnd)
		}
		if text != string(old) {
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return err
			}
			fmt.Println(path)
		}
	}
	return nil
}

// deployTo заливает бинарник на колонку. scp и sftp на колонке нет, поэтому файл идёт через stdin ssh; сначала во
// временное имя, потом переименование, чтобы обрыв связи не оставил битый файл.
func deployTo(bin, target string) error {
	host, port := target, "2222"
	if i := strings.LastIndexByte(target, ':'); i > 0 {
		host, port = target[:i], target[i+1:]
	}
	name := filepath.Base(bin)
	dst := "/tmp/openmediad/" + strings.TrimSuffix(name, ".exe") // рабочий каталог демона в ОЗУ: исчезнет при перезагрузке
	f, err := os.Open(bin)
	if err != nil {
		return err
	}
	defer f.Close()
	remote := fmt.Sprintf("mkdir -p /tmp/openmediad && cat > %[1]s.new && chmod +x %[1]s.new && mv %[1]s.new %[1]s && sha256sum %[1]s", dst)
	cmd := exec.Command("ssh", "-p", port, "root@"+host, remote)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = f, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	sum, err := fileSHA256(bin)
	if err != nil {
		return err
	}
	fmt.Printf("%s  (локальный файл)\nзалито в %s:%s. На колонке: %s start (до перезагрузки) или %s install\n", sum, host, dst, dst, dst)
	return nil
}
