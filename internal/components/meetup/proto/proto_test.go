package proto_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/components/meetup/proto/prototest"
)

func TestParseLink(t *testing.T) {
	cases := []struct {
		in         string
		host, room string
		secure     bool
		key, ws    string
	}{
		{"https://meetup.fferry.ru/conference.html?room=AbCdEfGhIjKlMnOpQrStUvWx", "meetup.fferry.ru", "AbCdEfGhIjKlMnOpQrStUvWx", true, "", "wss://meetup.fferry.ru/ws"},
		{"  мит-ап.рф/conference.html?room=vi5#k=abc_DEF-123 ", "xn----8sbwpsp.xn--p1ai", "vi5", true, "abc_DEF-123", "wss://xn----8sbwpsp.xn--p1ai/ws"},
		{"http://192.168.1.5/conference.html?room=r1", "192.168.1.5", "r1", false, "", "ws://192.168.1.5:9000"},
		{"http://localhost:8081/conference.html?room=r1", "localhost:8081", "r1", false, "", "ws://localhost:9000"},
		{"https://MeetUp.Example.com:8443/conference.html?x=1&room=r2", "meetup.example.com:8443", "r2", true, "", "wss://meetup.example.com:8443/ws"},
	}
	for _, c := range cases {
		l, err := proto.ParseLink(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if l.Host != c.host || l.Room != c.room || l.Secure != c.secure || l.Key != c.key || l.WSURL() != c.ws {
			t.Errorf("%q: получено %+v (ws %s)", c.in, l, l.WSURL())
		}
	}
	for _, bad := range []string{"", "https://meetup.example.com/", "ftp://x/conference.html?room=1", "https:///conference.html?room=1", "https://a..b/conference.html?room=1"} {
		if l, err := proto.ParseLink(bad); err == nil {
			t.Errorf("%q принята: %+v", bad, l)
		}
	}
}

// Punycode сверяется с общеизвестными именами (IDNA): ошибка здесь означала бы «сервер не найден» на домене с кириллицей.
func TestParseLinkPunycode(t *testing.T) {
	for in, want := range map[string]string{
		"президент.рф":     "xn--d1abbgf6aiiy.xn--p1ai",
		"пример.испытание": "xn--e1afmkfd.xn--80akhbyknj4f",
		"bücher.example":   "xn--bcher-kva.example",
		"МИТ-АП.РФ":        "xn----8sbwpsp.xn--p1ai",
	} {
		l, err := proto.ParseLink("https://" + in + "/conference.html?room=x")
		if err != nil || l.Host != want {
			t.Errorf("%s → %q, %v; ждали %q", in, l.Host, err, want)
		}
	}
}

// Display не показывает код комнаты целиком: код — это и есть секрет комнаты, а строка идёт в журнал и в status.
func TestLinkDisplayHidesRoomCode(t *testing.T) {
	l, _ := proto.ParseLink("https://meetup.example.com/conference.html?room=AbCdEfGhIjKlMnOpQrStUvWx#k=secretkey")
	d := l.Display()
	if strings.Contains(d, "AbCdEfGhIjKlMnOpQrStUvWx") || strings.Contains(d, "secretkey") || !strings.Contains(d, "meetup.example.com") {
		t.Fatalf("Display = %q", d)
	}
}

func join(t *testing.T, srv *prototest.Server, name string) *proto.Session {
	t.Helper()
	l, err := proto.ParseLink(srv.Link("room1"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := proto.Join(context.Background(), l, proto.Options{Name: name, WSURL: srv.WSURL()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("не дождались: %s", what)
		panic("unreachable")
	}
}

func TestJoinChatStateAudio(t *testing.T) {
	srv := prototest.New(t)
	s := join(t, srv, "Колонка")

	j := srv.Joins()[0]
	if j["room"] != "room1" || j["name"] != "Колонка" || j["mic"] != false || j["cam"] != false {
		t.Fatalf("join: %v", j)
	}
	if s.ID() != 7 {
		t.Fatalf("наш id %d", s.ID())
	}
	if p := s.Participants(); len(p) != 1 || p[0].Name != "Анна" || p[0].ID != 3 || p[0].UserID != 5 || !p[0].Mic {
		t.Fatalf("участники: %+v", p)
	}

	chats := make(chan proto.Chat, 8)
	roster := make(chan struct{}, 8)
	notices := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		done <- s.Run(proto.Handler{Chat: func(c proto.Chat) { chats <- c }, Roster: func() { roster <- struct{}{} }, Notice: func(r string) { notices <- r }})
	}()

	// чужое сообщение, зашифрованное сообщение, наше собственное (сервер возвращает его и отправителю)
	srv.Say(3, "Анна", "!next")
	if c := recv(t, chats, "чат"); c.Text != "!next" || c.Sender != "Анна" || c.SenderID != 3 || c.Own || c.Encrypted {
		t.Fatalf("чат: %+v", c)
	}
	srv.Say(3, "Анна", proto.ChatCipherPrefix+"AAAA")
	if c := recv(t, chats, "шифрованный чат"); !c.Encrypted {
		t.Fatalf("шифрованное сообщение не распознано: %+v", c)
	}
	if err := s.SendChat("привет"); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, srv.Chats, "наш текст на сервере"); got != "привет" {
		t.Fatalf("сервер получил %q", got)
	}
	if c := recv(t, chats, "наше сообщение"); !c.Own || c.Text != "привет" {
		t.Fatalf("своё сообщение: %+v", c)
	}

	// длинный текст обрезается до предела сервера
	s.SendChat(strings.Repeat("я", proto.MaxChatText+50))
	if got := recv(t, srv.Chats, "длинный текст"); len([]rune(got)) != proto.MaxChatText {
		t.Fatalf("длина %d символов", len([]rune(got)))
	}
	recv(t, chats, "эхо длинного текста")

	// список участников
	srv.Broadcast(map[string]any{"type": "participant_joined", "id": 9, "name": "Пётр", "mic": false, "cam": true})
	recv(t, roster, "вход участника")
	srv.Broadcast(map[string]any{"type": "participant_state", "id": 9, "mic": true})
	recv(t, roster, "состояние участника")
	if p := s.Participants(); len(p) != 2 || p[1].Name != "Пётр" || !p[1].Mic || !p[1].Cam || p[1].UserID != 0 {
		t.Fatalf("участники: %+v", p)
	}
	srv.Broadcast(map[string]any{"type": "participant_left", "id": 3})
	recv(t, roster, "выход участника")
	if p := s.Participants(); len(p) != 1 || p[0].ID != 9 {
		t.Fatalf("участники после выхода: %+v", p)
	}

	// состояние микрофона и звуковой кадр
	if err := s.SetMic(true); err != nil {
		t.Fatal(err)
	}
	if !recv(t, srv.Mic, "состояние микрофона") {
		t.Fatal("сервер получил mic=false")
	}
	at := time.UnixMilli(1751970000123)
	if err := s.SendAudio([]byte{0xF8, 1, 2, 3}, at); err != nil {
		t.Fatal(err)
	}
	f := recv(t, srv.Audio, "звуковой кадр")
	if f.Type != 4 || f.Flags != 0 || f.Codec != 4 || f.TimeMS != 1751970000123 || string(f.Payload) != "\xF8\x01\x02\x03" {
		t.Fatalf("кадр: %+v", f)
	}

	// чужое видео выбрасывается, не мешая тексту; некритичная ошибка — в Notice
	srv.SendBinary(make([]byte, 300_000))
	srv.Broadcast(map[string]any{"type": "error", "reason": "image_too_large"})
	if r := recv(t, notices, "некритичная ошибка"); r != "image_too_large" {
		t.Fatalf("notice %q", r)
	}
	if frames, bytes := s.Skipped(); frames != 1 || bytes != 300_000 {
		t.Fatalf("выброшено %d кадров, %d байт", frames, bytes)
	}

	// сервер выгоняет: Run возвращает причину
	srv.Broadcast(map[string]any{"type": "error", "reason": "room_closed"})
	err := recv(t, done, "конец Run")
	var je *proto.JoinError
	if !errors.As(err, &je) || je.Reason != "room_closed" || !proto.Fatal(je.Reason) {
		t.Fatalf("Run вернул %v", err)
	}
}

func TestJoinRefusedAndDropped(t *testing.T) {
	srv := prototest.New(t)
	l, _ := proto.ParseLink(srv.Link("нет"))
	srv.RefuseJoin("room_not_found")
	_, err := proto.Join(context.Background(), l, proto.Options{Name: "Колонка", WSURL: srv.WSURL()})
	var je *proto.JoinError
	if !errors.As(err, &je) || je.Reason != "room_not_found" {
		t.Fatalf("отказ: %v", err)
	}
	if proto.Fatal("room_offline") {
		t.Fatal("room_offline — не окончательная причина: комнату откроет владелец")
	}
	if _, err := proto.Join(context.Background(), l, proto.Options{WSURL: srv.WSURL()}); err == nil {
		t.Fatal("вход без имени принят")
	}

	srv.RefuseJoin("")
	s := join(t, srv, "Колонка")
	done := make(chan error, 1)
	go func() { done <- s.Run(proto.Handler{}) }()
	srv.Drop()
	if err := recv(t, done, "конец Run после обрыва"); err == nil {
		t.Fatal("обрыв связи не замечен")
	}

	// сервера нет вовсе
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := proto.Join(ctx, l, proto.Options{Name: "Колонка", WSURL: "ws://127.0.0.1:1/ws"}); err == nil {
		t.Fatal("соединение с закрытым портом открылось")
	}
}

func timeAt(ms int64) time.Time { return time.UnixMilli(ms) }
