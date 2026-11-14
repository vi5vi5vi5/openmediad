package meetup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/config"
)

// Команда терминала `openmediad meetup`: то же, что страница настроек, через её же API на этой колонке.

const commandHelp = `Колонка входит в конференцию MeetUp как участник: звук движка идёт в комнату, команды из чата управляют плеером.
То же самое делает страница настроек (адрес — ключ listen секции [meetup]).

  status     состояние: в комнате ли колонка, кто ещё в комнате, сколько звука отправлено
  join       войти в комнату по ссылке вида https://сервер/conference.html?room=КОД; -name — имя колонки в комнате
  leave      выйти из комнаты
  say        написать в чат комнаты от имени колонки
  audio      отдавать ли звук колонки в конференцию (нужен выход "meetup" в [route])
  chat       выполнять ли команды из чата (!play, !pause, !next …)
  alice      разрешить ли в чате свободные фразы Алисе (!алиса …, !play название)
  vol        громкость колонки в конференции
  login      войти в аккаунт MeetUp на сервере из сохранённой ссылки; пароль читается со стандартного ввода
             (openmediad meetup login ЛОГИН < файл-с-паролем) и не сохраняется: хранится только сессия
  logout     выйти из аккаунта
  room-password   пароль личной комнаты: одна строка со стандартного ввода; пустая строка стирает
  e2e        фраза сквозного шифрования: одна строка со стандартного ввода; пустая строка стирает.
             Ключ из ссылки-приглашения (…#k=КЛЮЧ) главнее фразы
  obey-new   слушаться ли команд участников, для которых нет личного правила (личные правила — на странице настроек)`

func init() {
	command.Register("MeetUp", command.Command{
		Name:    "meetup",
		Summary: "конференция MeetUp: войти в комнату, выйти, состояние",
		Usage:   "[status] | join ССЫЛКА [-name ИМЯ] | leave | say ТЕКСТ | audio|chat|alice|obey-new on|off | vol 0–100 | login ЛОГИН | logout | room-password | e2e",
		Help:    commandHelp,
		Run:     runCommand,
	})
}

func runCommand(c *command.Ctx, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		st, err := call(http.MethodGet, "/api/state", nil)
		if err != nil {
			return err
		}
		printState(c, st)
		return nil
	case "join":
		fs := command.Flags(c, "meetup join")
		name := fs.String("name", "", "имя колонки в комнате")
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return command.UsageError{Msg: "нужна ссылка на конференцию: openmediad meetup join ССЫЛКА [-name ИМЯ]"}
		}
		if err := command.Parse(fs, args[1:], 0); err != nil {
			return err
		}
		body := map[string]any{"link": args[0], "connect": true}
		if *name != "" {
			body["name"] = *name
		}
		return change(c, body)
	case "leave":
		return change(c, map[string]any{"connect": false})
	case "say":
		if len(args) == 0 {
			return command.UsageError{Msg: "нужен текст: openmediad meetup say ТЕКСТ"}
		}
		if _, err := call(http.MethodPost, "/api/say", map[string]any{"text": strings.Join(args, " ")}); err != nil {
			return err
		}
		c.Printf("отправлено в чат\n")
		return nil
	case "audio", "chat", "alice", "obey-new":
		if len(args) != 1 || args[0] != "on" && args[0] != "off" {
			return command.UsageError{Msg: fmt.Sprintf("openmediad meetup %s on|off", sub)}
		}
		return change(c, map[string]any{strings.ReplaceAll(sub, "-", "_"): args[0] == "on"})
	case "login":
		if len(args) != 1 {
			return command.UsageError{Msg: "openmediad meetup login ЛОГИН (пароль — одной строкой со стандартного ввода)"}
		}
		password, err := secretLine(c, "пароль аккаунта")
		if err != nil {
			return err
		}
		return post(c, "/api/account", map[string]any{"login": args[0], "password": password})
	case "logout":
		return post(c, "/api/account", map[string]any{"logout": true})
	case "room-password":
		password, err := secretLine(c, "пароль комнаты (пустая строка — стереть)")
		if err != nil {
			return err
		}
		return change(c, map[string]any{"room_password": password})
	case "e2e":
		phrase, err := secretLine(c, "фраза шифрования (пустая строка — стереть)")
		if err != nil {
			return err
		}
		return change(c, map[string]any{"e2e_phrase": phrase})
	case "vol":
		if len(args) != 1 {
			return command.UsageError{Msg: "openmediad meetup vol 0–100"}
		}
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 || v > 100 {
			return command.UsageError{Msg: "громкость — число от 0 до 100"}
		}
		return change(c, map[string]any{"volume": v})
	}
	return command.UsageError{Msg: "неизвестное действие " + strconv.Quote(sub)}
}

func change(c *command.Ctx, body map[string]any) error { return post(c, "/api/settings", body) }

func post(c *command.Ctx, path string, body map[string]any) error {
	st, err := call(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	printState(c, st)
	return nil
}

// secretLine читает секрет одной строкой со стандартного ввода: в аргументах команды он остался бы в истории оболочки.
func secretLine(c *command.Ctx, what string) (string, error) {
	fmt.Fprintf(c.Stderr, "%s: ", what)
	line, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && line == "" && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func printState(c *command.Ctx, st State) {
	c.Printf("конференция: колонка %s", st.Status)
	if st.Room != "" {
		c.Printf(" (%s)", st.Room)
	}
	c.Printf("\n")
	if st.Error != "" {
		c.Printf("  ошибка:     %s\n", st.Error)
	}
	if st.Hint != "" {
		c.Printf("  заметка:    %s\n", st.Hint)
	}
	if st.Account != "" {
		c.Printf("  аккаунт:    %s\n", st.Account)
	} else {
		c.Printf("  аккаунт:    нет, имя в комнате — %s\n", st.Name)
	}
	secrets := "пароль комнаты " + setOrNot(st.HasPassword) + ", фраза шифрования " + setOrNot(st.HasPhrase)
	switch st.E2E {
	case e2eLink:
		secrets += "; сессия шифруется ключом из ссылки"
	case e2ePhrase:
		secrets += "; сессия шифруется по фразе"
	}
	c.Printf("  секреты:    %s\n", secrets)
	c.Printf("  звук:       %s, громкость %d %%\n", onOff(st.Audio), st.Volume)
	c.Printf("  чат:        команды %s, фразы Алисе %s, новых участников %s, личных правил %d\n", onOff(st.Chat), onOff(st.Alice),
		map[bool]string{true: "слушается", false: "не слушается"}[st.ObeyNew], len(st.Rules))
	if st.Phase == phaseJoined {
		names := make([]string, 0, len(st.Participants))
		for _, p := range st.Participants {
			name := p.Name
			if !p.Obey {
				name += " (не слушается)"
			}
			names = append(names, name)
		}
		c.Printf("  участники:  %s\n", strings.Join(names, ", "))
		c.Printf("  отправлено: кадров %d, пропало %d, потоков сейчас %d, команд из чата %d\n", st.Sent, st.Dropped, st.Streams, st.Commands)
	}
}

func setOrNot(b bool) string {
	if b {
		return "задан(а)"
	}
	return "нет"
}

func onOff(b bool) string {
	if b {
		return "включено"
	}
	return "выключено"
}

// apiAddr — адрес страницы настроек на этой колонке (из файла настроек).
func apiAddr() (string, error) {
	cfg, err := config.LoadFile(config.Path())
	if err != nil {
		return "", err
	}
	mc, ok := cfg.Components[Name].(*Config)
	if !ok || !cfg.Enabled(Name) {
		return "", fmt.Errorf("компонент meetup выключен в настройках ([meetup] enabled)")
	}
	if mc.Listen == "" {
		return "", fmt.Errorf("страница настроек выключена ([meetup] listen пуст): команде не с чем говорить")
	}
	host, port, err := net.SplitHostPort(mc.Listen)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func call(method, path string, body map[string]any) (State, error) {
	var st State
	addr, err := apiAddr()
	if err != nil {
		return st, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		return st, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return st, fmt.Errorf("демон не отвечает на %s (запущен ли он и включён ли компонент meetup?): %w", addr, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return st, fmt.Errorf("%s", e.Error)
		}
		return st, fmt.Errorf("колонка ответила %s", resp.Status)
	}
	json.Unmarshal(raw, &st)
	return st, nil
}
