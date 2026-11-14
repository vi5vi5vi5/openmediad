package meetup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/core/control"
)

// Команды чата. Колонка ведёт себя как музыкальный бот: участник пишет «!next», колонка выполняет и отвечает в чат.
//
// Управление воспроизведением (пауза, дальше, назад) идёт через ядро команд (control.Hub), как кнопки наушников: чей сейчас
// звук, решает ядро. Всё, что запускает музыку или требует понимания фразы, говорится Алисе текстом, как голосом: «!play» —
// это «Алиса, играй», поэтому выбор источника (Яндекс Музыка, Spotify) работает так же, как голосом.

const (
	maxPhrase  = 200             // предел длины фразы Алисе из чата, символов
	askTimeout = 5 * time.Second // сколько ждём, пока платформа примет фразу или команду
)

type chatCommand struct {
	names []string // первое имя — основное
	args  string   // подсказка по аргументам для справки
	help  string
	run   func(c *Component, arg string) string // ответ в чат ("" — молча)
}

var chatCommands []chatCommand

func init() {
	chatCommands = []chatCommand{
		{[]string{"play", "играй", "включи"}, "[что]", "продолжить музыку; с запросом — попросить Алису включить", (*Component).cmdPlay},
		{[]string{"pause", "пауза", "стоп", "stop"}, "", "пауза", func(c *Component, _ string) string { return c.cmdPlayer(control.Pause, "⏸ Пауза") }},
		{[]string{"next", "дальше", "skip"}, "", "следующий трек", func(c *Component, _ string) string { return c.cmdPlayer(control.Next, "⏭ Дальше") }},
		{[]string{"prev", "назад"}, "", "предыдущий трек", func(c *Component, _ string) string { return c.cmdPlayer(control.Prev, "⏮ Назад") }},
		{[]string{"np", "что"}, "", "что сейчас играет", (*Component).cmdNowPlaying},
		{[]string{"vol", "громкость"}, "[0–100 | + | -]", "громкость колонки в конференции", (*Component).cmdVolume},
		{[]string{"алиса", "alice", "a"}, "<фраза>", "сказать Алисе фразу, она ответит голосом в конференцию", (*Component).cmdAlice},
		{[]string{"help", "помощь", "команды"}, "", "этот список", (*Component).cmdHelp},
	}
}

func findCommand(name string) *chatCommand {
	for i := range chatCommands {
		for _, n := range chatCommands[i].names {
			if n == name {
				return &chatCommands[i]
			}
		}
	}
	return nil
}

// allowed решает, слушаться ли участника: личное правило для него, а если правила нет — правило для новых участников
// (Settings.ObeyNew). Участника под аккаунтом узнают по аккаунту; у вошедшего без аккаунта есть только имя, и его может взять
// кто угодно. tell — участнику стоит сказать об отказе (один раз за сессию).
func (c *Component) allowed(m proto.Chat) (ok, tell bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok, _ = c.set.obeys(ruleKey(c.link.Host, m.UserID, m.Sender)); ok {
		return true, false
	}
	if c.told == nil || c.told[m.SenderID] {
		return false, false
	}
	c.told[m.SenderID] = true
	return false, true
}

// onChat — сообщение чата (горутина чтения: только разбор, выполнение — в очереди команд).
func (c *Component) onChat(m proto.Chat) {
	if m.Own {
		return
	}
	if m.Encrypted {
		c.encrypted.Add(1)
		return
	}
	c.mu.Lock()
	on, prefix := c.set.Chat, c.cfg.Prefix
	c.mu.Unlock()
	if !on || prefix == "" {
		return
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(m.Text), prefix)
	if !ok {
		return
	}
	name, arg, _ := strings.Cut(rest, " ")
	name, arg = strings.ToLower(name), strings.Join(strings.Fields(arg), " ")
	if !isWord(name) { // «!!!», «!?» — не команда
		return
	}
	if ok, tell := c.allowed(m); !ok {
		if tell {
			c.say(m.Sender + ", колонка вас не слушается: так настроил её владелец.")
		}
		return
	}
	cmd := findCommand(name)
	c.mu.Lock()
	ok, warn := c.bucket.take(time.Now())
	c.mu.Unlock()
	if !ok {
		if warn {
			c.say("Слишком много команд подряд, подождите несколько секунд.")
		}
		return
	}
	if cmd == nil {
		c.say(fmt.Sprintf("Не знаю команду «%s%s». Список команд: %shelp", prefix, name, prefix))
		return
	}
	job := func() {
		c.commands.Add(1)
		c.logf("конференция: команда %s%s от участника «%s»", prefix, cmd.names[0], m.Sender)
		if reply := cmd.run(c, arg); reply != "" {
			c.say(reply)
		}
	}
	select {
	case c.jobs <- job:
	default:
		c.say("Колонка занята предыдущими командами, повторите чуть позже.")
	}
}

func isWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func (c *Component) hello() string {
	return fmt.Sprintf("Колонка на связи. Команды: %shelp", c.config().Prefix)
}

func (c *Component) cmdHelp(string) string {
	p := c.config().Prefix
	var b strings.Builder
	b.WriteString("Команды колонки:")
	for _, cmd := range chatCommands {
		b.WriteString("\n" + p + cmd.names[0])
		if cmd.args != "" {
			b.WriteString(" " + cmd.args)
		}
		b.WriteString(" — " + cmd.help)
	}
	return b.String()
}

// phrase говорит Алисе фразу текстом, как голосом.
func (c *Component) phrase(text string) error {
	if c.ask == nil {
		return fmt.Errorf("нет связи с Алисой")
	}
	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()
	return c.ask(ctx, text)
}

func (c *Component) freePhrases() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set.Alice
}

func trimPhrase(s string) string {
	if utf8.RuneCountInString(s) > maxPhrase {
		s = string([]rune(s)[:maxPhrase])
	}
	return s
}

func (c *Component) cmdPlay(arg string) string {
	if arg == "" {
		if err := c.phrase("играй"); err != nil {
			return "Не получилось: " + err.Error()
		}
		return "▶ Алиса, играй"
	}
	if !c.freePhrases() {
		return "Запросы к Алисе из чата выключены в настройках колонки. Без запроса команда работает."
	}
	arg = trimPhrase(arg)
	if err := c.phrase("включи " + arg); err != nil {
		return "Не получилось: " + err.Error()
	}
	return "▶ Алиса, включи «" + arg + "»"
}

func (c *Component) cmdAlice(arg string) string {
	if !c.freePhrases() {
		return "Фразы Алисе из чата выключены в настройках колонки."
	}
	if arg == "" {
		return "Напишите фразу после команды, например: " + c.config().Prefix + "алиса какая погода"
	}
	if err := c.phrase(trimPhrase(arg)); err != nil {
		return "Не получилось: " + err.Error()
	}
	return "" // ответит сама Алиса, голосом
}

func (c *Component) cmdPlayer(cmd control.Cmd, done string) string {
	if c.hub == nil {
		return "Управление плеером недоступно."
	}
	by, err := c.hub.Command(cmd, control.API)
	switch {
	case err != nil:
		return "Не получилось: платформа колонки не приняла команду."
	case by == "":
		return "Некому выполнить: сейчас ничего не играет."
	}
	return done
}

func (c *Component) cmdVolume(arg string) string {
	c.mu.Lock()
	v := c.set.Volume
	c.mu.Unlock()
	switch arg {
	case "":
		return fmt.Sprintf("Громкость колонки в конференции: %d %%", v)
	case "+":
		v += 10
	case "-":
		v -= 10
	default:
		n, err := strconv.Atoi(strings.TrimSuffix(arg, "%"))
		if err != nil || n < 0 || n > 100 {
			return "Громкость — число от 0 до 100, либо + или -."
		}
		v = n
	}
	set, _ := c.Update(func(s *Settings) { s.Volume = v })
	return fmt.Sprintf("🔊 Громкость колонки в конференции: %d %%", set.Volume)
}

// cmdNowPlaying — что играет: название от музыки платформы (Яндекс) или от плеера движка (control.State.Title; Spotify).
func (c *Component) cmdNowPlaying(string) string {
	if c.mach != nil && c.mach.Playing() {
		for _, p := range c.mach.Snapshot() {
			// у радио исполнителя нет, и название приходит как « — Серебряный дождь»
			if title := strings.TrimLeft(p.Title, " —-"); title != "" {
				return "Сейчас играет: " + title
			}
		}
		return "Сейчас играет музыка платформы."
	}
	if c.hub != nil {
		for _, p := range c.hub.Players() {
			switch {
			case !p.State.Playing:
			case p.State.Title != "":
				return "Сейчас играет: " + p.State.Title
			default:
				return "Сейчас играет " + p.Name + "."
			}
		}
	}
	return "Сейчас ничего не играет."
}

// bucket ограничивает частоту команд из чата: запас в несколько команд подряд и медленное пополнение.
type bucket struct {
	size     float64 // сколько команд можно выполнить подряд
	tokens   float64
	at       time.Time
	warnedAt time.Time
}

const (
	bucketSize   = 6
	bucketRefill = 2 * time.Second // одна команда раз в столько
)

func newBucket(size float64) bucket { return bucket{size: size, tokens: size} }

// take — можно ли выполнить команду сейчас; warn — стоит ли сказать об отказе (не чаще раза в 10 с).
func (b *bucket) take(now time.Time) (ok, warn bool) {
	if !b.at.IsZero() {
		b.tokens = min(b.size, b.tokens+float64(now.Sub(b.at))/float64(bucketRefill))
	}
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, false
	}
	if now.Sub(b.warnedAt) > 10*time.Second {
		b.warnedAt = now
		return false, true
	}
	return false, false
}
