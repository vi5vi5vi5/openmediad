package meetup

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
)

// Живая проверка на настоящем сервере MeetUp (без колонки): компонент входит в комнату, играет в неё звук, похожий на музыку,
// и выполняет команды чата на поддельном плеере. Слушать и писать в чат — из веб-клиента в той же комнате.
//
//	OMD_MEETUP_LINK=http://localhost:8088/conference.html?room=КОД go test -run TestLive -v -count=1 ./internal/components/meetup
//
// Необязательно: OMD_MEETUP_WS — адрес WebSocket вручную; OMD_MEETUP_SECONDS — сколько секунд играть (по умолчанию 60);
// OMD_MEETUP_LISTEN — адрес страницы настроек (например 127.0.0.1:6702), чтобы пощёлкать её во время проверки;
// OMD_MEETUP_LOGIN и OMD_MEETUP_PASSWORD — войти под аккаунтом; OMD_MEETUP_ROOM_PASSWORD — пароль комнаты;
// OMD_MEETUP_PHRASE — фраза сквозного шифрования (ключ в ссылке, …#k=КЛЮЧ, работает и без неё).
func TestLive(t *testing.T) {
	link := os.Getenv("OMD_MEETUP_LINK")
	if link == "" {
		t.Skip("живая проверка: задайте OMD_MEETUP_LINK (ссылка на комнату настоящего сервера MeetUp)")
	}
	seconds, _ := strconv.Atoi(os.Getenv("OMD_MEETUP_SECONDS"))
	if seconds <= 0 {
		seconds = 60
	}
	cfg := *defaults()
	cfg.Listen = os.Getenv("OMD_MEETUP_LISTEN")
	cfg.WSURL = os.Getenv("OMD_MEETUP_WS")
	c := newComponent(cfg, filepath.Join(t.TempDir(), "meetup.json"), t.Logf)
	c.tls = tlsConfig()
	ps := controltest.Playing()
	ps.Title = "Проверочный исполнитель — Проверочный трек"
	player := controltest.NewPlayer(ps)
	c.hub = &control.Hub{}
	c.hub.Add("поддельный плеер", player)
	c.ask = func(_ context.Context, text string) error { t.Logf("фраза Алисе: %q", text); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); c.Stop() }()
	if _, err := c.Update(func(s *Settings) {
		s.Link, s.Name = link, "Колонка (проверка)"
		s.RoomPassword, s.Phrase = os.Getenv("OMD_MEETUP_ROOM_PASSWORD"), os.Getenv("OMD_MEETUP_PHRASE")
	}); err != nil {
		t.Fatal(err)
	}
	if login := os.Getenv("OMD_MEETUP_LOGIN"); login != "" {
		if err := c.Login(ctx, login, os.Getenv("OMD_MEETUP_PASSWORD")); err != nil {
			t.Fatalf("вход в аккаунт: %v", err)
		}
	}
	c.Update(func(s *Settings) { s.Connect = true })
	for deadline := time.Now().Add(15 * time.Second); c.Snapshot().Phase != phaseJoined; time.Sleep(50 * time.Millisecond) {
		if st := c.Snapshot(); st.Phase == phaseFailed || time.Now().After(deadline) {
			t.Fatalf("не вошла в комнату: %+v", st)
		}
	}
	t.Logf("в комнате: %s", c.StatusLine())

	out, err := c.Open(audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	mono := musicLike(1)
	chunk := make([]byte, 0, 4*960)
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	for off := 0; time.Now().Before(end); off = (off + 960) % len(mono) {
		chunk = chunk[:0]
		for _, v := range mono[off : off+960] {
			chunk = binary.LittleEndian.AppendUint16(chunk, uint16(v))
			chunk = binary.LittleEndian.AppendUint16(chunk, uint16(v))
		}
		if _, err := out.Write(chunk); err != nil {
			t.Fatalf("поток снят: %v (%s)", err, c.StatusLine())
		}
	}
	out.Drain()
	st := c.Snapshot()
	t.Logf("итог: %+v", st)
	t.Logf("плеер получил: %+v", player.Done())
	if st.Sent < uint64(seconds*40) || st.Dropped > uint64(seconds) {
		t.Fatalf("отправлено кадров %d, пропало %d за %d с", st.Sent, st.Dropped, seconds)
	}
}
