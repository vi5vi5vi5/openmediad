// build — сборка openmediad с выбором компонентов. Работает на Windows, Linux и macOS: make и sh не нужны.
//
//	go run ./tools/build                           под колонку (linux/arm64), компоненты по умолчанию → dist/openmediad
//	go run ./tools/build -with pprof               добавить опытные компоненты
//	go run ./tools/build -without debug,spectrum   убрать стандартные
//	go run ./tools/build -host                     под этот компьютер (для проверок)
//	go run ./tools/build -list                     компоненты и их теги
//	go run ./tools/build -check                    собрать три набора: по умолчанию, минимальный, максимальный
//	go run ./tools/build -dev                      собрать openmediad-dev (инструменты разработчика)
//	go run ./tools/build -deploy 192.168.1.12      собрать и залить на колонку по ssh (порт 2222) в /tmp/openmediad/
//	go run ./tools/build -verify                   целостность vendor/: хэши файлов и go mod verify
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

	"openmediad/internal/registry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}

type opts struct {
	with, without []string
	host, dev     bool
	out           string
}

func run() error {
	with := flag.String("with", "", "добавить компоненты через запятую (опытные или ранее убранные)")
	without := flag.String("without", "", "убрать компоненты через запятую")
	host := flag.Bool("host", false, "собрать под этот компьютер, а не под колонку")
	list := flag.Bool("list", false, "показать компоненты и теги")
	check := flag.Bool("check", false, "собрать три набора компонентов (проверка, что все сочетания собираются)")
	dev := flag.Bool("dev", false, "собрать openmediad-dev вместо openmediad")
	deploy := flag.String("deploy", "", "после сборки залить на колонку: адрес[:ssh-порт] (по умолчанию порт 2222)")
	verify := flag.Bool("verify", false, "проверить целостность vendor/")
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
	case *check:
		return checkAll()
	}
	o := opts{with: split(*with), without: split(*without), host: *host, dev: *dev, out: *out}
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

// tagsFor переводит -with/-without в теги сборки.
func tagsFor(with, without []string) ([]string, error) {
	var tags []string
	for _, n := range with {
		c, err := component(n)
		if err != nil {
			return nil, err
		}
		if !c.Default {
			tags = append(tags, c.Tag())
		}
	}
	for _, n := range without {
		c, err := component(n)
		if err != nil {
			return nil, err
		}
		if c.Default {
			tags = append(tags, c.Tag())
		}
	}
	return tags, nil
}

func component(name string) (registry.Info, error) {
	c, ok := registry.Lookup(name)
	if !ok {
		return c, fmt.Errorf("нет компонента %q (список: go run ./tools/build -list)", name)
	}
	if !c.Ready {
		return c, fmt.Errorf("компонент %q ещё не реализован (фаза %d плана)", name, c.Phase)
	}
	return c, nil
}

func printList() {
	fmt.Println("компонент    вид        по умолчанию  тег              описание")
	for _, c := range registry.Catalog {
		def := "нет"
		if c.Default {
			def = "да"
		}
		tag := c.Tag()
		if !c.Ready {
			def, tag = "—", fmt.Sprintf("(фаза %d)", c.Phase)
		}
		fmt.Printf("%-12s %-10s %-13s %-16s %s\n", c.Name, c.Kind, def, tag, c.Summary)
	}
}

// build собирает бинарник и возвращает путь к нему.
func build(o opts) (string, error) {
	tags, err := tagsFor(o.with, o.without)
	if err != nil {
		return "", err
	}
	name := "openmediad"
	if o.dev {
		name = "openmediad-dev"
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
	fmt.Printf("собрано: %s (%s/%s, %.1f МБ, теги: %s)\n", out, goos, goarch, float64(st.Size())/(1<<20), tagList(tags))
	return out, nil
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "нет"
	}
	return strings.Join(tags, ",")
}

func goBuild(goos, goarch string, tags []string, pkg, out string) error {
	ldflags := fmt.Sprintf("-s -w -X openmediad/internal/buildinfo.Version=%s -X openmediad/internal/buildinfo.Date=%s",
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

// checkAll собирает под колонку три набора компонентов и инструменты разработчика.
func checkAll() error {
	tmp, err := os.MkdirTemp("", "openmediad-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var all, defaults []string
	for _, c := range registry.Catalog {
		if !c.Ready {
			continue
		}
		all = append(all, c.Name)
		if c.Default {
			defaults = append(defaults, c.Name)
		}
	}
	sets := []struct {
		label         string
		with, without []string
		dev           bool
	}{
		{label: "по умолчанию"},
		{label: "минимальный (только ядро)", without: defaults},
		{label: "максимальный (все готовые компоненты)", with: all},
		{label: "инструменты разработчика", dev: true},
	}
	for i, s := range sets {
		fmt.Printf("== %s\n", s.label)
		o := opts{with: s.with, without: s.without, dev: s.dev, out: filepath.Join(tmp, fmt.Sprintf("bin%d", i))}
		if _, err := build(o); err != nil {
			return fmt.Errorf("набор «%s»: %w", s.label, err)
		}
	}
	fmt.Println("все наборы собираются")
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
