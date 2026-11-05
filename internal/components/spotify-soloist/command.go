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
	"openmediad/internal/libs/wsclient"
)

// Команда терминала `openmediad spotify …` приходит вместе с компонентом: нет компонента в сборке — нет и команды.
func init() {
	command.Register("Spotify", command.Command{
		Name:    "spotify",
		Summary: "Spotify (Soloist): установка, ключ, обновление, состояние",
		Usage:   "setup [-from КАТАЛОГ] [-force] | setkey [-file ФАЙЛ] | login [-url АДРЕС | -check | -clear] | alias [add НАЗВАНИЕ ССЫЛКА | rm НАЗВАНИЕ] | phrases [test ФРАЗА | reset] | rename ИМЯ | update | status | clean [-all] | uninstall -yes",
		Help: `Spotify на колонке работает через официальный движок Soloist. Он закрытый, поэтому проект его не содержит:
установка скачивает его с CDN Spotify прямо на колонку.

  setup      скачать Soloist и glibc (из пакета Debian), записать шим; повторный запуск докачивает недостающее.
             -from КАТАЛОГ — без сети: взять soloist_release_arm32.tar.gz и libc6_*.deb из каталога; -force — переустановить
  setkey     записать ключ Soloist (одна строка со стандартного ввода: openmediad spotify setkey < key.txt); без перенаправления
             спросит ключ. Ключ выпускается в Developer Dashboard Spotify (нужен Premium): https://developer.spotify.com/dashboard/soloist
  login      вход в Web API Spotify для лайков («Алиса, лайк»). Нужен Client ID вашего приложения из Developer Dashboard
             (https://developer.spotify.com/dashboard; Redirect URI приложения: http://127.0.0.1:8888/callback; свой аккаунт
             добавьте в User Management): openmediad config set spotify-soloist.client_id ИДЕНТИФИКАТОР. Затем
             «openmediad spotify login» покажет ссылку; откройте её на компьютере, разрешите доступ, а адрес, на который вернёт
             браузер (страница не откроется, это нормально), отдайте команде: openmediad spotify login -url 'http://127.0.0.1:8888/callback?code=…'.
             -check — проверить вход и доступ к Web API по шагам. -clear — забыть вход.
             Spotify закрывает Web API для адресов некоторых стран (в ответ «unavailable in this country»): тогда нужен прокси,
             openmediad config set spotify-soloist.web_proxy http://адрес:порт (или socks5://адрес:порт)
  alias      свои названия для «Алиса, включи …» без поиска (работает и без Web API): openmediad spotify alias add "кино" <ссылка>,
             alias rm "кино", alias (список). Ссылка: в приложении «Поделиться» → «Копировать ссылку» (плейлист, альбом, исполнитель, трек)
  phrases    как называют Spotify в голосовых просьбах («спотифай», «спотик», «Spotify»), предлоги и целые фразы запуска. Лежат в файле
             phrases.txt (правится руками, перечитывается сам). Без параметров — путь, что в файле и не ошибся ли разбор; test "включи музыку в спотике" —
             как поймётся фраза; reset — вернуть встроенные фразы
  raw        (отладка) отправить Soloist команду WebSocket и показать его ответы: openmediad spotify raw '{"type":"command","command":"get_state"}'
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
		return command.UsageError{Msg: "нужна подкоманда: setup, setkey, login, alias, phrases, rename, update, status, clean, uninstall"}
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
	case "login":
		fs := command.Flags(c, "spotify login")
		back := fs.String("url", "", "адрес, на который Spotify вернул браузер после разрешения (или только значение code); «-» — со стандартного ввода")
		clear := fs.Bool("clear", false, "забыть сохранённый вход")
		check := fs.Bool("check", false, "проверить вход и доступ к Web API (по шагам)")
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		w := newWebAPI(p, func() string { return cfg.ClientID })
		w.proxy = func() string { return cfg.WebProxy }
		switch {
		case *check:
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			lines, ok := w.check(ctx)
			for _, l := range lines {
				say("%s", l)
			}
			if !ok {
				return errors.New("лайки не заработают, причина выше")
			}
			say("Всё в порядке: лайки должны ставиться.")
			return nil
		case *clear:
			w.forget()
			say("Вход в Web API Spotify удалён.")
			return nil
		case *back == "":
			link, err := w.begin()
			if err != nil {
				return err
			}
			say("1. Откройте ссылку на компьютере или телефоне и разрешите доступ:")
			say("")
			say("%s", link)
			say("")
			say("2. Браузер попробует открыть адрес http://127.0.0.1:8888/callback?code=… — страница не откроется, это нормально.")
			say("   Скопируйте этот адрес целиком из строки браузера и выполните (адрес в одинарных кавычках):")
			say("")
			say("   openmediad spotify login -url 'АДРЕС'")
			say("")
			say("Ссылка действует 30 минут. В Dashboard у приложения должен быть Redirect URI %s.", webRedirect)
			return nil
		}
		input := *back
		if input == "-" {
			line, err := io.ReadAll(io.LimitReader(c.Stdin, 8192))
			if err != nil {
				return err
			}
			input = string(line)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := w.finish(ctx, input); err != nil {
			return err
		}
		say("Вход выполнен. Лайки работают сразу (перезапуск не нужен): «Алиса, лайк» при играющем Spotify.")
		if !cfg.Like {
			say("Внимание: like = false в настройках, лайки выключены.")
		}
		return nil
	case "raw":
		fs := command.Flags(c, "spotify raw")
		wait := fs.Duration("wait", 3*time.Second, "сколько слушать ответы Soloist")
		if err := command.Parse(fs, args[1:], 1); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return command.UsageError{Msg: "нужна команда в JSON: openmediad spotify raw '{\"type\":\"command\",\"command\":\"get_state\"}'"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := wsclient.Dial(ctx, fmt.Sprintf("127.0.0.1:%d", cfg.WSPort), "/")
		if err != nil {
			return fmt.Errorf("Soloist не отвечает на 127.0.0.1:%d (демон запущен? Spotify настроен?): %w", cfg.WSPort, err)
		}
		defer conn.Close()
		time.AfterFunc(*wait, func() { conn.Close() })
		if err := conn.WriteText(fs.Arg(0)); err != nil {
			return err
		}
		for {
			m, err := conn.ReadMessage()
			if err != nil {
				return nil
			}
			text := string(m)
			if len(text) > 700 {
				text = text[:700] + "…"
			}
			say("%s", text)
		}
	case "alias":
		store := newAliasStore(p)
		sub := "list"
		if len(args) > 1 {
			sub = args[1]
		}
		switch sub {
		case "list":
			items, err := store.list()
			if err != nil {
				return err
			}
			if len(items) == 0 {
				say("Своих названий пока нет. Добавить: openmediad spotify alias add \"кино\" https://open.spotify.com/artist/…")
			}
			for _, it := range items {
				say("«%s» → %s", it[0], it[1])
			}
			return nil
		case "add":
			if len(args) != 4 {
				return command.UsageError{Msg: "нужны название и ссылка: openmediad spotify alias add \"кино\" https://open.spotify.com/artist/…"}
			}
			key, uri, err := store.set(args[2], args[3])
			if err != nil {
				return command.UsageError{Msg: err.Error()}
			}
			say("Теперь «Алиса, включи %s» запускает %s", key, uri)
			if !cfg.PlayByName {
				say("Внимание: play_by_name = false в настройках, просьбы по названию выключены.")
			}
			return nil
		case "rm":
			if len(args) != 3 {
				return command.UsageError{Msg: "нужно название: openmediad spotify alias rm \"кино\""}
			}
			ok, err := store.remove(args[2])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("названия «%s» нет", normalize(args[2]))
			}
			say("Удалено.")
			return nil
		}
		return command.UsageError{Msg: "alias: list, add НАЗВАНИЕ ССЫЛКА или rm НАЗВАНИЕ"}
	case "phrases":
		return runPhrases(c, p, args[1:])
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
