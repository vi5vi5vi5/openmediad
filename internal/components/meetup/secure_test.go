package meetup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tphakala/go-opus/opus"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/components/meetup/proto/prototest"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
)

// connect входит в комнату, не разбирая приветствие (в шифрованной комнате оно зашифровано).
func (s *stand) connect() {
	s.t.Helper()
	if _, err := s.c.Update(func(set *Settings) { set.Link, set.Connect = s.srv.Link(roomCode), true }); err != nil {
		s.t.Fatal(err)
	}
	s.waitPhase(phaseJoined)
}

// Вход под аккаунтом: пароль меняется на сессию, сессия лежит только в файле состояния и уходит только своему серверу.
func TestAccountLogin(t *testing.T) {
	s := newStand(t)
	uid := s.srv.AddAccount("kolonka", "secret-pass", "Станция Мини")
	ctx := context.Background()

	if err := s.c.Login(ctx, "kolonka", "secret-pass"); err == nil || !strings.Contains(err.Error(), "ссылку") {
		t.Fatalf("вход без ссылки: %v", err)
	}
	s.c.Update(func(set *Settings) { set.Link = s.srv.Link(roomCode) })
	if err := s.c.Login(ctx, "kolonka", "не тот"); err == nil || !strings.Contains(err.Error(), "неверный логин или пароль") {
		t.Fatalf("неверный пароль: %v", err)
	}
	if st := s.c.Snapshot(); st.Account != "" {
		t.Fatalf("после неудачного входа аккаунт %q", st.Account)
	}
	if err := s.c.Login(ctx, "kolonka", "secret-pass"); err != nil {
		t.Fatal(err)
	}

	s.srv.RequireAccount(true) // без аккаунта сюда не пускают
	s.connect()
	recv(t, s.srv.Chats, "приветствие")
	st := s.c.Snapshot()
	if !st.AccountInUse || !strings.HasPrefix(st.Account, "kolonka на ") || st.Hint != "" {
		t.Fatalf("состояние под аккаунтом: %+v", st)
	}
	if !strings.Contains(s.c.StatusLine(), "под аккаунтом") {
		t.Fatalf("строка состояния: %q", s.c.StatusLine())
	}
	// секрет остаётся в файле состояния и не попадает в состояние (страница, status -v, отладочная страница)
	js, _ := json.Marshal(st)
	if strings.Contains(string(js), "session-") || strings.Contains(string(js), "secret-pass") {
		t.Fatalf("состояние выдаёт сессию или пароль: %s", js)
	}
	file, _ := os.ReadFile(s.c.path)
	if !strings.Contains(string(file), "session-kolonka") || strings.Contains(string(file), "secret-pass") {
		t.Fatalf("файл состояния: сессия должна быть, пароля быть не должно: %s", file)
	}
	set, _ := loadSettings(s.c.path, "Колонка")
	if set.Account.UserID != uid || set.Account.Name != "Станция Мини" {
		t.Fatalf("сохранённый аккаунт: %+v", set.Account)
	}

	// другой сервер: сессия ему не отправляется
	other := prototest.New(t)
	s.c.mu.Lock()
	s.c.cfg.WSURL = other.WSURL()
	s.c.mu.Unlock()
	s.c.Update(func(set *Settings) { set.Link = other.Link(roomCode) })
	wait(t, "вход на другой сервер", func() bool { return other.Clients() == 1 })
	recv(t, other.Chats, "приветствие на другом сервере")
	if other.SessionCookies() != 0 {
		t.Fatal("сессия аккаунта ушла чужому серверу")
	}
	if st := s.c.Snapshot(); st.AccountInUse || st.Hint != "" {
		t.Fatalf("на чужом сервере: %+v", st)
	}

	// сессия истекла на своём сервере: колонка входит без аккаунта и говорит об этом
	s.srv.RequireAccount(false)
	s.srv.ExpireSessions()
	s.c.mu.Lock()
	s.c.cfg.WSURL = s.srv.WSURL()
	s.c.mu.Unlock()
	s.c.Update(func(set *Settings) { set.Link = s.srv.Link(roomCode) })
	wait(t, "вход с истёкшей сессией", func() bool {
		st := s.c.Snapshot()
		return st.Phase == phaseJoined && strings.Contains(st.Hint, "Сессия аккаунта истекла")
	})
	if s.c.Snapshot().AccountInUse {
		t.Fatal("истёкшая сессия показана как вход под аккаунтом")
	}

	// выход из аккаунта: сессия забыта, колонка перезаходит без неё
	cookies := s.srv.SessionCookies()
	s.c.Logout(ctx)
	wait(t, "перезаход без аккаунта", func() bool {
		st := s.c.Snapshot()
		return st.Phase == phaseJoined && st.Account == "" && st.Hint == ""
	})
	if s.srv.SessionCookies() != cookies {
		t.Fatal("после выхода из аккаунта кука всё ещё отправляется")
	}
	if file, _ := os.ReadFile(s.c.path); strings.Contains(string(file), "session-") {
		t.Fatalf("после выхода сессия осталась в файле: %s", file)
	}
}

// Пароль комнаты: без него отказ с понятной причиной и без повторов, с ним — вход.
func TestRoomPasswordSetting(t *testing.T) {
	s := newStand(t)
	s.srv.RequirePassword("сезам")
	s.c.Update(func(set *Settings) { set.Link, set.Connect = s.srv.Link(roomCode), true })
	s.waitPhase(phaseFailed)
	if st := s.c.Snapshot(); !strings.Contains(st.Error, "пароль комнаты") || st.HasPassword {
		t.Fatalf("без пароля: %+v", st)
	}
	joins := len(s.srv.Joins())
	time.Sleep(150 * time.Millisecond)
	if len(s.srv.Joins()) != joins {
		t.Fatal("после отказа по паролю колонка продолжает стучаться")
	}
	s.c.Update(func(set *Settings) { set.RoomPassword = "сезам" })
	s.waitPhase(phaseJoined)
	st := s.c.Snapshot()
	js, _ := json.Marshal(st)
	if !st.HasPassword || strings.Contains(string(js), "сезам") {
		t.Fatalf("состояние: %s", js)
	}
}

// Шифрованная комната: приветствие, ответы и звук уходят зашифрованными, шифрованные команды выполняются; чужой ключ
// виден по подсказке. Ключ берётся из фразы или из ссылки-приглашения.
func TestEncryptedRoom(t *testing.T) {
	s := newStand(t)
	key := proto.KeyFromPhrase("наша фраза", roomCode)
	peer, _ := proto.NewCipher(key)
	stranger, _ := proto.NewCipher(proto.KeyFromPhrase("чужая фраза", roomCode))
	open := func(what string) string {
		t.Helper()
		wire := recv(t, s.srv.Chats, what)
		text, ok := peer.OpenChat(wire)
		if !ok {
			t.Fatalf("%s ушло открытым текстом или под другим ключом: %q", what, wire)
		}
		return text
	}

	s.c.Update(func(set *Settings) { set.Phrase = "наша фраза" })
	s.connect()
	if hello := open("приветствие"); !strings.Contains(hello, "!help") {
		t.Fatalf("приветствие: %q", hello)
	}
	if st := s.c.Snapshot(); st.E2E != e2ePhrase || !st.HasPhrase || !strings.Contains(s.c.StatusLine(), "шифрование") {
		t.Fatalf("состояние: %+v", st)
	}

	s.srv.Say(3, "Анна", peer.SealChat("!next"))
	if reply := open("ответ на шифрованную команду"); !strings.Contains(reply, "Дальше") {
		t.Fatalf("ответ: %q", reply)
	}
	if done := s.player.Done(); len(done) != 1 || done[0].Cmd != control.Next {
		t.Fatalf("плеер получил: %+v", done)
	}
	s.srv.Say(4, "Чужой", stranger.SealChat("!next"))
	wait(t, "подсказка о чужом ключе", func() bool {
		st := s.c.Snapshot()
		return st.Encrypted == 1 && strings.Contains(st.Hint, "другой ключ")
	})
	if s.player.Count() != 1 {
		t.Fatal("команда под чужим ключом выполнена")
	}

	// звук: кадры с флагом «зашифровано» открываются ключом комнаты и декодируются
	out, err := s.c.Open(audio.Mono48)
	if err != nil {
		t.Fatal(err)
	}
	out.Write(tone(audio.Mono48, 200*time.Millisecond))
	out.Drain()
	dec, _ := opus.NewDecoder(48000, 1)
	pcm := make([]int16, 5760)
	for i := 0; i < 5; i++ {
		f := recv(t, s.srv.Audio, "звуковой кадр")
		if f.Flags != 2 {
			t.Fatalf("кадр без флага шифрования: %+v", f)
		}
		plain, ok := peer.Open(f.Type, f.Codec, f.Payload)
		if !ok {
			t.Fatal("звук не открывается ключом комнаты")
		}
		if n, err := dec.Decode(plain, pcm); err != nil || n != 960 {
			t.Fatalf("расшифрованный кадр: %d отсчётов, %v", n, err)
		}
	}

	// ключ из ссылки-приглашения главнее фразы
	linkKey := proto.KeyFromPhrase("ключ из ссылки", "x")
	viaLink, _ := proto.NewCipher(linkKey)
	s.c.Update(func(set *Settings) {
		set.Link = s.srv.Link(roomCode) + "#k=" + base64.RawURLEncoding.EncodeToString(linkKey)
	})
	wait(t, "перезаход с ключом из ссылки", func() bool { return s.c.Snapshot().E2E == e2eLink })
	wire := recv(t, s.srv.Chats, "приветствие под ключом из ссылки")
	if text, ok := viaLink.OpenChat(wire); !ok || !strings.Contains(text, "!help") {
		t.Fatalf("приветствие под ключом из ссылки: %q", wire)
	}

	// испорченный ключ в ссылке: открытым текстом колонка в такую комнату не входит
	joins := len(s.srv.Joins())
	s.c.Update(func(set *Settings) { set.Link = s.srv.Link(roomCode) + "#k=abc123" })
	s.waitPhase(phaseFailed)
	if st := s.c.Snapshot(); !strings.Contains(st.Error, "ключ шифрования в ссылке") || len(s.srv.Joins()) != joins {
		t.Fatalf("испорченный ключ: %+v, входов %d", st, len(s.srv.Joins())-joins)
	}
}

// Комната без ключа у колонки: шифрованные сообщения не читаются, и колонка подсказывает, что нужен ключ.
func TestEncryptedChatWithoutKey(t *testing.T) {
	s := newStand(t)
	s.join()
	peer, _ := proto.NewCipher(proto.KeyFromPhrase("фраза", roomCode))
	s.srv.Say(3, "Анна", peer.SealChat("!next"))
	wait(t, "подсказка о ключе", func() bool {
		st := s.c.Snapshot()
		return st.Encrypted == 1 && strings.Contains(st.Hint, "ключа у колонки нет")
	})
	if s.player.Count() != 0 {
		t.Fatal("шифрованная команда выполнена без ключа")
	}
}

// Кого слушаться: личные правила и правило для новых. Участника под аккаунтом узнают по аккаунту, а не по имени.
func TestObeyRules(t *testing.T) {
	s := newStand(t)
	s.join()
	host := strings.TrimPrefix(s.srv.Link(""), "http://")
	host = host[:strings.Index(host, "/")]
	deny, allow := false, true
	account := func(id int64) uint32 { return 0x80000000 + uint32(id) }

	// по умолчанию слушается всех
	people := s.c.Snapshot().Participants
	if len(people) != 1 || people[0].Name != "Анна" || !people[0].Obey || people[0].Ruled || people[0].Account || people[0].Key != host+"|n|анна" {
		t.Fatalf("участники: %+v", people)
	}
	s.command("!next")

	// личный запрет: один раз колонка говорит об этом, дальше молчит; остальных слушается
	s.c.Update(func(set *Settings) { set.setRule(people[0].Key, "Анна", &deny) })
	if r := s.command("!next"); !strings.Contains(r, "не слушается") || !strings.Contains(r, "Анна") {
		t.Fatalf("ответ на запрещённую команду: %q", r)
	}
	s.srv.Say(3, "Анна", "!next")
	quiet(t, s.srv.Chats, 150*time.Millisecond, "повторный ответ запрещённому участнику")
	s.srv.Say(9, "Пётр", "!next")
	if r := recv(t, s.srv.Chats, "ответ Петру"); !strings.Contains(r, "Дальше") {
		t.Fatalf("ответ Петру: %q", r)
	}
	if p := s.c.Snapshot().Participants[0]; p.Obey || !p.Ruled {
		t.Fatalf("Анна в состоянии: %+v", p)
	}
	if s.player.Count() != 2 {
		t.Fatalf("команд выполнено %d", s.player.Count())
	}

	// новых не слушаться: Пётр молчит, пока нет личного разрешения
	s.c.Update(func(set *Settings) { set.ObeyNew = false })
	if r := func() string { s.srv.Say(9, "Пётр", "!next"); return recv(t, s.srv.Chats, "отказ Петру") }(); !strings.Contains(r, "не слушается") {
		t.Fatalf("ответ Петру при «новых не слушаться»: %q", r)
	}
	// аккаунт: разрешение привязано к аккаунту, смена имени его не снимает, а чужой аккаунт с тем же именем не получает
	s.c.Update(func(set *Settings) { set.setRule(ruleKey(host, 42, ""), "Вася", &allow) })
	s.srv.Say(account(42), "Вася", "!next")
	recv(t, s.srv.Chats, "ответ Васе")
	s.srv.Say(account(42), "Уже не Вася", "!next")
	recv(t, s.srv.Chats, "ответ Васе под новым именем")
	s.srv.Say(account(43), "Вася", "!next")
	if r := recv(t, s.srv.Chats, "отказ тёзке"); !strings.Contains(r, "не слушается") {
		t.Fatalf("тёзка с другим аккаунтом получил: %q", r)
	}
	// гость без аккаунта, назвавшийся именем человека с аккаунтом, разрешения не получает
	s.srv.Say(11, "Вася", "!next")
	if r := recv(t, s.srv.Chats, "отказ гостю-тёзке"); !strings.Contains(r, "не слушается") {
		t.Fatalf("гость-тёзка получил: %q", r)
	}
	if s.player.Count() != 4 {
		t.Fatalf("команд выполнено %d: %+v", s.player.Count(), s.player.Done())
	}

	// правило забыто — действует правило для новых; правила пережили перезапуск
	set, _ := loadSettings(s.c.path, "Колонка")
	if set.ObeyNew || len(set.Rules) != 2 {
		t.Fatalf("файл состояния: %+v", set)
	}
	s.c.Update(func(set *Settings) { set.setRule(people[0].Key, "", nil); set.ObeyNew = true })
	if r := s.command("!next"); !strings.Contains(r, "Дальше") {
		t.Fatalf("после снятия запрета: %q", r)
	}
	if st := s.c.Snapshot(); len(st.Rules) != 1 || st.Rules[0].Name != "Вася" || !st.ObeyNew {
		t.Fatalf("правила: %+v", st.Rules)
	}
}

// «Что играет» называет трек плеера движка, если плеер знает название (Spotify).
func TestNowPlayingTitle(t *testing.T) {
	s := newStand(t)
	s.join()
	if r := s.command("!np"); !strings.Contains(r, "плеер") {
		t.Fatalf("без названия: %q", r)
	}
	st := controltest.Playing()
	st.Title = "Кино — Группа крови"
	s.player.Set(st)
	if r := s.command("!np"); r != "Сейчас играет: Кино — Группа крови" {
		t.Fatalf("с названием: %q", r)
	}
	s.player.Set(control.State{Ready: true})
	if r := s.command("!np"); !strings.Contains(r, "ничего не играет") {
		t.Fatalf("тишина: %q", r)
	}
}

func TestRules(t *testing.T) {
	set := defaultSettings("Колонка")
	yes, no := true, false
	if ok, ruled := set.obeys("k"); !ok || ruled {
		t.Fatal("по умолчанию слушается всех, личных правил нет")
	}
	set.setRule("k", "Анна", &no)
	set.setRule("k", "Анна", &no) // повтор не плодит записи
	if ok, ruled := set.obeys("k"); ok || !ruled || len(set.Rules) != 1 {
		t.Fatalf("запрет: %+v", set.Rules)
	}
	set.setRule("k", "Анна", &yes)
	if ok, _ := set.obeys("k"); !ok || len(set.Rules) != 1 {
		t.Fatalf("разрешение поверх запрета: %+v", set.Rules)
	}
	for i := 0; i < maxRules+10; i++ {
		set.setRule(ruleKey("h", int64(i+1), ""), "x", &yes)
	}
	if len(set.Rules) != maxRules {
		t.Fatalf("правил %d", len(set.Rules))
	}
	if ruleKey("h", 0, "  Анна   К. ") != "h|n|анна к." || ruleKey("h", 7, "Анна") != "h|u|7" {
		t.Fatal("ключ правила")
	}
}
