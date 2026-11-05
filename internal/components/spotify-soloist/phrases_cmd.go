package spotifysoloist

import (
	"errors"
	"os"
	"strings"

	"openmediad/internal/core/cli/command"
	playctl "openmediad/internal/core/control"
)

// runPhrases — `openmediad spotify phrases [test ФРАЗА | reset]`: посмотреть файл фраз, проверить, как поймётся фраза, вернуть встроенные.
func runPhrases(c *command.Ctx, p paths, args []string) error {
	say := func(f string, a ...any) { c.Printf(f+"\n", a...) }
	path := p.phrases()
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "reset":
		if err := newBookStore(path, nil).write(); err != nil {
			return err
		}
		say("Записаны встроенные фразы: %s", path)
		return nil
	case "test":
		if len(args) != 2 {
			return command.UsageError{Msg: `нужна фраза: openmediad spotify phrases test "включи музыку в спотике"`}
		}
		b, source, err := readBook(path)
		if err != nil {
			return err
		}
		say("Фразы: %s", source)
		for _, l := range classifyPhrase(b, args[1]) {
			say("%s", l)
		}
		return nil
	case "":
	default:
		return command.UsageError{Msg: "phrases: без параметров, test ФРАЗА или reset"}
	}
	b, source, err := readBook(path)
	if err != nil {
		return err
	}
	say("Файл фраз: %s", path)
	say("Читаются: %s (%s)", source, b.summary())
	say("Названия сервиса: %s", strings.Join(b.sorted(b.names), ", "))
	say("Предлоги перед названием: %s", strings.Join(b.sorted(b.preps), ", "))
	if len(b.starts) == 0 {
		say("Целые фразы запуска: нет (сами собираются «включи / запусти / открой + название»)")
	} else {
		say("Целые фразы запуска: %s", strings.Join(b.sorted(b.starts), "; "))
	}
	say("")
	say("Правится руками (nano %s), перечитывается сам. Проверить фразу: openmediad spotify phrases test \"включи спотифай\"", path)
	return nil
}

// readBook читает файл фраз; нет файла — встроенные. source говорит, откуда книга.
func readBook(path string) (*phraseBook, string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return builtinBook, "встроенные (файла пока нет: он создаётся при запуске демона; сейчас создать — openmediad spotify phrases reset)", nil
	}
	if err != nil {
		return nil, "", err
	}
	b, warns, err := parsePhraseBook(string(data))
	if err != nil {
		return nil, "", errors.New(path + ": " + err.Error() + " (в работающем демоне остаются прежние фразы)")
	}
	source := "файл"
	if len(warns) > 0 {
		source += "; внимание: " + strings.Join(warns, "; ")
	}
	return b, source, nil
}

// classifyPhrase говорит, что сделает компонент с фразой: чья она и что с ней будет. Тот же разбор, что у голоса.
func classifyPhrase(b *phraseBook, text string) []string {
	out := []string{"После разбора: «" + normalize(text) + "»"}
	if cmd, ok := playctl.ParsePhrase(text); ok {
		return append(out, "Управление плеером: "+cmd.String()+". Выполняет ядро: если звук у Spotify — Spotify, иначе фраза уходит Алисе.")
	}
	if a := parsePhrase(text); a != actNone {
		return append(out, "Фраза Spotify: "+a.String()+".")
	}
	r, ok := b.parsePlayRequest(text)
	switch {
	case !ok:
		return append(out, "Не фраза Spotify: уйдёт Алисе без изменений.")
	case r.Start:
		return append(out, "Запустит Spotify (продолжит прошлую сессию или сыграет music_uri), даже если играет Яндекс.Музыка; Алиса промолчит.")
	}
	how := "голая просьба: выполняется после того, как облако подтвердит, что это музыка"
	if r.Explicit {
		how = "явная просьба про музыку: выполняется и без подтверждения облака"
	}
	return append(out, "Просьба по названию: ищу «"+r.Query+"» ("+r.Kind.String()+"); "+how+".")
}
