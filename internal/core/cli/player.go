package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"openmediad/internal/core/ctlsock"
)

// Команды управления через сокет демона (docs/API.md): то же, что доступно своим программам.

var playerActions = map[string]string{
	"pause": ctlsock.ActionPause, "resume": ctlsock.ActionResume, "play": ctlsock.ActionResume, "toggle": ctlsock.ActionToggle,
	"next": ctlsock.ActionNext, "prev": ctlsock.ActionPrev,
}

var playerCmd = Command{
	Name:    "player",
	Summary: "кто играет; пауза, продолжить, следующий и предыдущий трек",
	Usage:   "[pause | resume | toggle | next | prev] [-json]",
	Help: `Без действия — кто играет сейчас: плееры движка (Spotify и другие), музыка платформы (Яндекс.Музыка) и кому достанется «продолжи».

С действием — команда плееру, чей бы ни был звук: играет плеер движка — выполнит он; играет музыка платформы или ничего — команда
уходит платформе, как нажатие кнопки на колонке. Это тот же путь, что у кнопок наушников.

  openmediad player            кто играет
  openmediad player toggle     пауза, если играет, и продолжить, если стоит
  openmediad player next       следующий трек

Из своей программы то же самое доступно через сокет демона: docs/API.md.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "player")
		asJSON := fs.Bool("json", false, "ответ строкой JSON")
		// действие стоит перед флагами: flag разбирает флаги только до первого слова
		action := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			action, args = args[0], args[1:]
		}
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		if action == "" {
			var p ctlsock.Playing
			if err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "players"}, &p); err != nil {
				return err
			}
			if *asJSON {
				b, _ := json.Marshal(p)
				c.Printf("%s\n", b)
				return nil
			}
			printPlaying(c, p)
			return nil
		}
		act, ok := playerActions[action]
		if !ok {
			return UsageError{Msg: fmt.Sprintf("неизвестное действие %q: pause, resume, toggle, next или prev", action)}
		}
		var res ctlsock.ControlResult
		if err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "control", Action: act}, &res); err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.Marshal(res)
			c.Printf("%s\n", b)
			return nil
		}
		c.Printf("%s: %s\n", act, whoName(res.By))
		return nil
	},
}

// platformMusic — имя музыки платформы в ответах демона (control.OwnerPlatform).
const platformMusic = "platform-music"

func whoName(by string) string {
	if by == platformMusic {
		return "передано музыке платформы"
	}
	return "выполнил плеер " + by
}

func printPlaying(c *Ctx, p ctlsock.Playing) {
	for _, pl := range p.Players {
		state := "не готов (нет связи или входа)"
		switch {
		case pl.Sounding:
			state = "играет"
		case pl.Playing:
			state = "запускается"
		case pl.Paused && pl.Resumable:
			state = "на паузе (сессия его)"
		case pl.Paused:
			state = "на паузе"
		case pl.Ready:
			state = "готов, молчит"
		}
		c.Printf("%-18s %s\n", pl.Name+":", state)
	}
	if len(p.Players) == 0 {
		c.Printf("плееров движка в сборке нет\n")
	}
	if p.PlatformPlaying {
		c.Printf("%-18s играет\n", "музыка платформы:")
	} else {
		c.Printf("%-18s молчит\n", "музыка платформы:")
	}
	for _, t := range p.Platform {
		line := "  " + t.State
		if t.Title != "" {
			line += ": " + t.Title
		}
		if t.DurMs > 0 {
			line += fmt.Sprintf(" (%s из %s)", clock(t.PosMs), clock(t.DurMs))
		}
		c.Printf("%s\n", line)
	}
	switch p.Owner {
	case "":
		c.Printf("«продолжи» достанется: пока никто не играл\n")
	case platformMusic:
		c.Printf("«продолжи» достанется: музыке платформы\n")
	default:
		c.Printf("«продолжи» достанется: %s\n", p.Owner)
	}
	if p.Dialog {
		c.Printf("идёт диалог с Алисой: музыка приглушена\n")
	}
}

func clock(ms uint64) string {
	d := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

var eventsCmd = Command{
	Name:    "events",
	Summary: "события плееров и диалога с Алисой по мере появления",
	Usage:   "[-json]",
	Help: `Печатает события, пока не прервут (Ctrl+C): пауза, продолжение и перемотка у любого плеера (откуда бы ни пришла команда,
в том числе из приложения Spotify), начало и конец диалога с Алисой.

С -json — по строке JSON на событие: удобно читать своей программой.

  openmediad events -json | while read -r e; do …; done

Поля событий — docs/API.md.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "events")
		asJSON := fs.Bool("json", false, "события строками JSON")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		return ctlsock.Stream(ctlsock.SocketPath(), ctlsock.Request{Cmd: "events"}, func(data json.RawMessage) bool {
			if *asJSON {
				c.Printf("%s\n", data)
				return true
			}
			var e ctlsock.Event
			if json.Unmarshal(data, &e) == nil {
				c.Printf("%s\n", eventLine(e))
			}
			return true
		})
	},
}

func eventLine(e ctlsock.Event) string {
	at := time.UnixMilli(e.AtMs).Format("15:04:05.000")
	switch e.Type {
	case ctlsock.EventDialog:
		if e.On {
			return at + " диалог с Алисой начался"
		}
		return at + " диалог с Алисой кончился"
	case ctlsock.EventPlayer:
		line := fmt.Sprintf("%s %s: %s (откуда: %s)", at, e.Player, e.Action, e.From)
		if e.PlatformDisplay {
			line += "; анимацию на дисплее покажет прошивка"
		}
		return line
	}
	return at + " " + e.Type
}

var askCmd = Command{
	Name:    "ask",
	Summary: "сказать Алисе фразу текстом, как голосом",
	Usage:   "ФРАЗА",
	Help: `Передаёт Алисе фразу так, как если бы её произнесли: она ответит голосом и выполнит просьбу.

  openmediad ask включи свет
  openmediad ask "громкость 3"`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "ask")
		if err := parse(fs, args, -1); err != nil {
			return err
		}
		text := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if text == "" {
			return UsageError{Msg: "нужна фраза: openmediad ask включи свет"}
		}
		var msg string
		if err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "ask", Text: text}, &msg); err != nil {
			return err
		}
		c.Printf("%s\n", msg)
		return nil
	},
}
