package proto_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/components/meetup/proto/prototest"
)

func link(t *testing.T, srv *prototest.Server) proto.Link {
	t.Helper()
	l, err := proto.ParseLink(srv.Link("room1"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Вход под аккаунтом: логин и пароль меняются на куку сессии, с ней сервер привязывает соединение к аккаунту.
func TestAccountLoginAndJoin(t *testing.T) {
	srv := prototest.New(t)
	uid := srv.AddAccount("anna", "secret-pass", "Анна К.")
	l := link(t, srv)
	ctx := context.Background()

	var ae *proto.APIError
	if _, err := proto.Login(ctx, l, "anna", "не тот", nil); !errors.As(err, &ae) || ae.Code != "wrong_credentials" || !strings.Contains(err.Error(), "неверный логин или пароль") {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, err := proto.Login(ctx, l, "", "x", nil); err == nil {
		t.Fatal("пустой логин принят")
	}
	acc, err := proto.Login(ctx, l, " Anna ", "secret-pass", nil)
	if err != nil || acc.Token == "" || acc.UserID != uid || acc.Login != "anna" || acc.Name != "Анна К." {
		t.Fatalf("вход: %+v, %v", acc, err)
	}

	// сервер «только для своих»: без куки не пускает, с кукой пускает под именем из профиля
	srv.RequireAccount(true)
	var je *proto.JoinError
	if _, err := proto.Join(ctx, l, proto.Options{Name: "Колонка", WSURL: srv.WSURL()}); !errors.As(err, &je) || je.Reason != "anonymous_forbidden" || !proto.Fatal(je.Reason) {
		t.Fatalf("вход без аккаунта: %v", err)
	}
	s, err := proto.Join(ctx, l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Session: acc.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Account() || s.ID() != 0x80000000+uint32(uid) || proto.UserIDOf(s.ID()) != uid {
		t.Fatalf("соединение не привязано к аккаунту: id %d", s.ID())
	}
	done := make(chan error, 1)
	chats := make(chan proto.Chat, 4)
	go func() { done <- s.Run(proto.Handler{Chat: func(c proto.Chat) { chats <- c }}) }()
	s.SendChat("привет")
	if c := recv(t, chats, "своё сообщение"); c.Sender != "Анна К." || c.UserID != uid || !c.Own {
		t.Fatalf("сообщение от аккаунта: %+v", c)
	}
	srv.Say(9, "Гость", "привет")
	if c := recv(t, chats, "сообщение гостя"); c.UserID != 0 {
		t.Fatalf("у гостя без аккаунта UserID %d", c.UserID)
	}

	// сессия истекла: сервер пускает как участника без аккаунта, и это видно
	srv.RequireAccount(false)
	srv.ExpireSessions()
	s2, err := proto.Join(ctx, l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Session: acc.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Account() {
		t.Fatal("истёкшая сессия принята за аккаунт")
	}

	// выход из аккаунта делает куку недействительной
	acc2, _ := proto.Login(ctx, l, "anna", "secret-pass", nil)
	if err := proto.Logout(ctx, l, acc2.Token, nil); err != nil {
		t.Fatal(err)
	}
	s3, err := proto.Join(ctx, l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Session: acc2.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if s3.Account() {
		t.Fatal("после выхода из аккаунта кука ещё действует")
	}
}

func TestRoomPassword(t *testing.T) {
	srv := prototest.New(t)
	srv.RequirePassword("сезам")
	l := link(t, srv)
	var je *proto.JoinError
	for _, pw := range []string{"", "не тот"} {
		if _, err := proto.Join(context.Background(), l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Password: pw}); !errors.As(err, &je) || je.Reason != "wrong_password" || !proto.Fatal(je.Reason) {
			t.Fatalf("пароль %q: %v", pw, err)
		}
	}
	if _, has := srv.Joins()[0]["password"]; has {
		t.Fatal("пустой пароль отправлен в join")
	}
	s, err := proto.Join(context.Background(), l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Password: "сезам"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

// Сквозное шифрование: наш чат и звук уходят зашифрованными и открываются тем же ключом у собеседника; шифрованный чат
// собеседника читается, а сообщение под чужим ключом помечается непрочитанным.
func TestEncryptedSession(t *testing.T) {
	srv := prototest.New(t)
	l := link(t, srv)
	key := proto.KeyFromPhrase("фраза", l.Room)
	mine, _ := proto.NewCipher(key)
	peer, _ := proto.NewCipher(key)
	stranger, _ := proto.NewCipher(proto.KeyFromPhrase("чужая", l.Room))

	s, err := proto.Join(context.Background(), l, proto.Options{Name: "Колонка", WSURL: srv.WSURL(), Cipher: mine})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chats := make(chan proto.Chat, 8)
	go s.Run(proto.Handler{Chat: func(c proto.Chat) { chats <- c }})

	s.SendChat("⏭ Дальше")
	wire := recv(t, srv.Chats, "наш текст на сервере")
	if !strings.HasPrefix(wire, proto.ChatCipherPrefix) || strings.Contains(wire, "Дальше") {
		t.Fatalf("сервер видит открытый текст: %q", wire)
	}
	if text, ok := peer.OpenChat(wire); !ok || text != "⏭ Дальше" {
		t.Fatalf("собеседник прочёл %q, %v", text, ok)
	}
	if c := recv(t, chats, "эхо своего сообщения"); c.Text != "⏭ Дальше" || c.Encrypted || !c.Own {
		t.Fatalf("своё сообщение: %+v", c)
	}

	srv.Say(3, "Анна", peer.SealChat("!next"))
	if c := recv(t, chats, "шифрованная команда"); c.Text != "!next" || c.Encrypted {
		t.Fatalf("команда собеседника: %+v", c)
	}
	srv.Say(4, "Чужой", stranger.SealChat("!next"))
	if c := recv(t, chats, "сообщение под чужим ключом"); c.Text != "" || !c.Encrypted {
		t.Fatalf("сообщение под чужим ключом: %+v", c)
	}
	srv.Say(3, "Анна", "открытый текст") // шифрование по желанию: открытые сообщения читаются как есть
	if c := recv(t, chats, "открытое сообщение"); c.Text != "открытый текст" || c.Encrypted {
		t.Fatalf("открытое сообщение: %+v", c)
	}

	// длинный ответ обрезается так, чтобы шифровка поместилась в предел сервера
	s.SendChat(strings.Repeat("я", 1500))
	wire = recv(t, srv.Chats, "длинная шифровка")
	if n := len([]rune(wire)); n > proto.MaxChatText {
		t.Fatalf("шифровка длиной %d символов не поместится в предел сервера", n)
	}
	if text, ok := peer.OpenChat(wire); !ok || len([]rune(text)) != 700 {
		t.Fatalf("длинное сообщение: %d символов, %v", len([]rune(text)), ok)
	}

	// звук: заголовок открыт, флаг «зашифровано», данные открываются ключом с AAD [тип, кодек]
	if err := s.SendAudio([]byte{0xF8, 1, 2, 3}, timeAt(1751970000123)); err != nil {
		t.Fatal(err)
	}
	f := recv(t, srv.Audio, "звуковой кадр")
	if f.Type != 4 || f.Codec != 4 || f.Flags != 2 || f.TimeMS != 1751970000123 {
		t.Fatalf("заголовок кадра: %+v", f)
	}
	if plain, ok := peer.Open(4, 4, f.Payload); !ok || string(plain) != "\xF8\x01\x02\x03" {
		t.Fatalf("кадр у собеседника: %x, %v", plain, ok)
	}
	if _, ok := stranger.Open(4, 4, f.Payload); ok {
		t.Fatal("чужой ключ открыл звук")
	}
}
