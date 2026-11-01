package spotifysoloist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/config"
	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/platform/disk"
	"openmediad/internal/core/platform/layout"
)

// Команда терминала `openmediad spotify …` приходит вместе с компонентом: нет компонента в сборке — нет и команды.
func init() {
	command.Register("Spotify", command.Command{
		Name:    "spotify",
		Summary: "Spotify (Soloist): установка, ключ, обновление, состояние",
		Usage:   "setup [-from КАТАЛОГ] [-force] | setkey [-file ФАЙЛ] | rename ИМЯ | update | status | clean [-all] | uninstall -yes",
		Help: `Spotify на колонке работает через официальный движок Soloist. Он закрытый, поэтому проект его не содержит:
установка скачивает его с CDN Spotify прямо на колонку.

  setup      скачать Soloist и glibc (из пакета Debian), записать шим; повторный запуск докачивает недостающее.
             -from КАТАЛОГ — без сети: взять soloist_release_arm32.tar.gz и libc6_*.deb из каталога; -force — переустановить
  setkey     записать ключ Soloist (одна строка со стандартного ввода: openmediad spotify setkey < key.txt); без перенаправления
             спросит ключ. Ключ выпускается в Developer Dashboard Spotify (нужен Premium): https://developer.spotify.com/dashboard/soloist
  rename     переименовать колонку в списке устройств Spotify на ходу (Soloist перезапускается за пару секунд):
             openmediad spotify rename "Кухня". То же: openmediad config set spotify-soloist.device_name "Кухня"
  update     скачать свежую сборку Soloist (каждая живёт 90 дней); ключ и вход остаются
  status     что установлено, срок сборки, место на /data, состояние в работающем демоне
  clean      удалить звуковой кэш (-all — ещё и вход, придётся выбирать устройство в приложении заново)
  uninstall  удалить всё (-yes обязателен)

После setup и setkey: openmediad restart, затем выберите устройство в приложении Spotify (телефон в той же Wi-Fi, VPN выключен).
При запуске компонент выставляет abi.cp15_barrier = 2 (без этого Soloist зависает); подробности — README компонента.`,
		Run: runCommand,
	})
}

func runCommand(c *command.Ctx, args []string) error {
	if len(args) == 0 {
		return command.UsageError{Msg: "нужна подкоманда: setup, setkey, rename, update, status, clean, uninstall"}
	}
	cfg := loadConfig(c)
	p := newPaths(cfg.Dir, ctlsock.RunDir(), cfg.KeyFile)
	say := func(f string, a ...any) { c.Printf(f+"\n", a...) }
	switch args[0] {
	case "setup":
		fs := command.Flags(c, "spotify setup")
		from := fs.String("from", "", "каталог с уже скачанными файлами (установка без сети)")
		force := fs.Bool("force", false, "переустановить всё")
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		in := newInstaller(p, say)
		in.fromDir = *from
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if err := in.Install(ctx, *force); err != nil {
			return err
		}
		say("")
		if !fileExists(p.key()) {
			say("Дальше: ключ Soloist — openmediad spotify setkey < key.txt (выпуск ключа: https://developer.spotify.com/dashboard/soloist)")
		}
		say("Потом: openmediad restart; в приложении Spotify выберите устройство «%s».", cfg.DeviceName)
		return nil
	case "setkey":
		fs := command.Flags(c, "spotify setkey")
		file := fs.String("file", "", "файл с ключом (по умолчанию — стандартный ввод)")
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		var r io.Reader = c.Stdin
		if *file != "" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		} else if f, ok := c.Stdin.(*os.File); ok && isTerminal(f) {
			c.Printf("Вставьте ключ Soloist и нажмите Enter: ")
			line, err := readLine(f)
			if err != nil {
				return err
			}
			r = strings.NewReader(line)
		}
		if err := p.setKey(r); err != nil {
			return err
		}
		say("Ключ записан в %s (права 0600). Он виден в командной строке процесса Soloist — это особенность Soloist.", p.key())
		say("Применить: openmediad restart")
		return nil
	case "rename":
		if len(args) != 2 {
			return command.UsageError{Msg: "нужно новое имя: openmediad spotify rename \"Кухня\""}
		}
		name := strings.TrimSpace(args[1])
		probe := *defaults()
		probe.DeviceName = name
		if err := probe.validate(); err != nil {
			return command.UsageError{Msg: err.Error()}
		}
		if err := command.SetConfig(c, Name+".device_name", name); err != nil {
			return err
		}
		say("В списке устройств Spotify колонка появится под новым именем через несколько секунд; в приложении может понадобиться обновить список.")
		return nil
	case "update":
		in := newInstaller(p, say)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if err := in.Update(ctx); err != nil {
			return err
		}
		say("Применить: openmediad restart")
		return nil
	case "status":
		return printStatus(c, cfg, p)
	case "clean":
		fs := command.Flags(c, "spotify clean")
		all := fs.Bool("all", false, "удалить и вход (data/)")
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		return cleanCache(c, p, *all)
	case "uninstall":
		fs := command.Flags(c, "spotify uninstall")
		yes := fs.Bool("yes", false, "подтверждение")
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		if !*yes {
			return command.UsageError{Msg: "это удалит Soloist, кэш, вход и ключ (" + p.dir + "): добавьте -yes"}
		}
		if daemonRunning() {
			say("внимание: демон работает; перед удалением лучше «openmediad stop»")
		}
		if err := os.RemoveAll(p.dir); err != nil {
			return err
		}
		say("удалено: %s", p.dir)
		return nil
	}
	return command.UsageError{Msg: "неизвестная подкоманда: " + args[0]}
}

// loadConfig читает настройки компонента из общего файла; нет файла или секции — умолчания.
func loadConfig(c *command.Ctx) Config {
	cfg, err := config.LoadFile(config.Path())
	if err == nil {
		if sc, ok := cfg.Components[Name].(*Config); ok && sc != nil {
			return *sc
		}
	}
	return *defaults()
}

func daemonRunning() bool {
	var st ctlsock.Status
	return ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "status"}, &st) == nil
}

func printStatus(c *command.Ctx, cfg Config, p paths) error {
	say := func(f string, a ...any) { c.Printf(f+"\n", a...) }
	say("каталог установки: %s", p.dir)
	if miss := p.missing(); len(miss) > 0 {
		say("не хватает: %s", strings.Join(miss, ", "))
	} else {
		say("установлено: всё необходимое (Soloist, glibc, шим, ключ)")
	}
	if m, err := readMeta(p.meta()); err == nil {
		say("Soloist %s, %s", orDash(m.Soloist.Version), expiryText(m, time.Now()))
		say("glibc: %s", orDash(m.Glibc.Package))
	}
	say("abi.cp15_barrier: %s (нужно 2; fix_cp15 = %v — демон выставляет при запуске)", cp15Value(layout.Root), cfg.FixCP15)
	if kb, err := disk.FreeKB(filepath.Dir(p.dir)); err == nil {
		say("свободно на /data: %d МБ (звуковой кэш растёт до ≈%d МБ)", kb/1024, cfg.CacheMB)
	}
	if size := dirSize(p.cache()); size > 0 {
		say("звуковой кэш: %d МБ", size>>20)
	}
	var st ctlsock.Status
	err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "status"}, &st)
	switch {
	case errors.Is(err, ctlsock.ErrNotRunning):
		say("демон не запущен")
	case err != nil:
		return err
	default:
		for _, cs := range st.Components {
			if cs.Name == Name {
				say("в демоне: %s", orDefault(cs.Line, map[bool]string{true: "работает", false: "не работает: " + cs.Error}[cs.Running]))
			}
		}
	}
	return nil
}

func cleanCache(c *command.Ctx, p paths, all bool) error {
	if daemonRunning() {
		return errors.New("демон работает и Soloist держит кэш открытым: сначала «openmediad stop»")
	}
	dirs := []string{p.cache(), filepath.Join(p.data(), "cache")}
	if all {
		dirs = []string{p.cache(), p.data()}
	}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
		c.Printf("удалено: %s\n", d)
	}
	os.MkdirAll(p.cache(), 0o755)
	os.MkdirAll(p.data(), 0o755)
	return nil
}

func dirSize(dir string) (n int64) {
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func readLine(f *os.File) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) < 4096 {
		n, err := f.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			b = append(b, one[0])
		}
		if err != nil {
			if len(b) == 0 {
				return "", fmt.Errorf("ключ не введён: %w", err)
			}
			break
		}
	}
	return string(b), nil
}
