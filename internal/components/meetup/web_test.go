package meetup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openmediad/internal/components/meetup/proto/prototest"
)

func newWebStand(t *testing.T) (*Component, http.Handler) {
	c := newComponent(*defaults(), filepath.Join(t.TempDir(), "meetup.json"), t.Logf)
	t.Cleanup(c.mix.Stop)
	return c, c.handler()
}

func request(h http.Handler, method, path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://192.168.1.12:6702"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWebSettings(t *testing.T) {
	c, h := newWebStand(t)
	if w := request(h, "GET", "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Колонка в MeetUp") {
		t.Fatalf("страница: %d", w.Code)
	}

	link := "https://meetup.example.com/conference.html?room=AbCdEfGhIjKlMnOpQrStUvWx#k=secretkey"
	w := request(h, "POST", "/api/settings", `{"link":"`+link+`","name":"Станция","volume":70,"alice":false}`, nil)
	if w.Code != 200 {
		t.Fatalf("настройки: %d %s", w.Code, w.Body)
	}
	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Name != "Станция" || st.Volume != 70 || st.Alice || !st.Chat || !st.HasLink || st.Connect {
		t.Fatalf("состояние: %+v, %v", st, err)
	}
	// ссылка — секрет комнаты: API её не возвращает
	for _, path := range []string{"/api/state"} {
		body := request(h, "GET", path, "", nil).Body.String()
		if strings.Contains(body, "AbCdEfGhIjKlMnOpQrStUvWx") || strings.Contains(body, "secretkey") {
			t.Fatalf("%s выдаёт ссылку: %s", path, body)
		}
	}
	set, err := loadSettings(c.path, "Колонка")
	if err != nil || set.Link != link || set.Name != "Станция" || set.Volume != 70 {
		t.Fatalf("файл состояния: %+v, %v", set, err)
	}

	if w := request(h, "POST", "/api/settings", `{"link":"ftp://x","connect":true}`, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("негодная ссылка: %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/settings", `{не json`, nil); w.Code != 400 {
		t.Fatalf("негодный JSON: %d", w.Code)
	}
	if w := request(h, "POST", "/api/say", `{"text":"привет"}`, nil); w.Code != http.StatusConflict {
		t.Fatalf("сообщение без комнаты: %d", w.Code)
	}
	if w := request(h, "POST", "/api/say", `{"text":"  "}`, nil); w.Code != 400 {
		t.Fatalf("пустое сообщение: %d", w.Code)
	}
}

// Изменить настройки может сама страница или программа, но не чужая веб-страница в браузере хозяина.
func TestWebGuard(t *testing.T) {
	c, h := newWebStand(t)
	body := `{"volume":10}`
	cases := map[string]func(*http.Request){
		"форма без JSON":    func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"чужой Origin":      func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"подставной домен":  func(r *http.Request) { r.Host = "evil.example:6702" },
		"подставной Origin": func(r *http.Request) { r.Host = "evil.example"; r.Header.Set("Origin", "http://evil.example") },
	}
	for name, edit := range cases {
		if w := request(h, "POST", "/api/settings", body, edit); w.Code != http.StatusForbidden {
			t.Errorf("%s: код %d", name, w.Code)
		}
	}
	if w := request(h, "GET", "/api/settings", "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET на изменение: %d", w.Code)
	}
	if w := request(h, "GET", "/", "", func(r *http.Request) { r.Host = "evil.example" }); w.Code != http.StatusNotFound {
		t.Errorf("страница по подставному домену: %d", w.Code)
	}
	if c.Snapshot().Volume != 50 {
		t.Fatalf("отклонённый запрос изменил громкость: %d", c.Snapshot().Volume)
	}
	// своя страница: Origin совпадает с адресом колонки
	if w := request(h, "POST", "/api/settings", body, func(r *http.Request) { r.Header.Set("Origin", "http://192.168.1.12:6702") }); w.Code != 200 || c.Snapshot().Volume != 10 {
		t.Fatalf("запрос своей страницы: %d, громкость %d", w.Code, c.Snapshot().Volume)
	}
}

func TestTrustedHost(t *testing.T) {
	for host, want := range map[string]bool{
		"192.168.1.12:6702": true, "127.0.0.1": true, "[::1]:6702": true, "localhost:6702": true, "alice": true,
		"station.local:6702": true, "колонка.lan": true, "evil.example": false, "192.168.1.12.evil.example:6702": false, "": false,
	} {
		if got := trustedHost(host); got != want {
			t.Errorf("%q: %v", host, got)
		}
	}
}

func TestBucket(t *testing.T) {
	b := newBucket(2)
	now := time.Unix(1000, 0)
	for i := 0; i < 2; i++ {
		if ok, _ := b.take(now); !ok {
			t.Fatalf("команда %d отвергнута", i)
		}
	}
	if ok, warn := b.take(now); ok || !warn {
		t.Fatalf("третья подряд: выполнена %v, предупреждение %v", ok, warn)
	}
	if ok, warn := b.take(now.Add(time.Second)); ok || warn {
		t.Fatalf("четвёртая: выполнена %v, второе предупреждение %v", ok, warn)
	}
	if ok, _ := b.take(now.Add(time.Second + bucketRefill)); !ok {
		t.Fatal("после паузы команда отвергнута")
	}
}

// Секреты страница принимает, но не возвращает; личные правила ставятся и забываются; вход в аккаунт идёт через сервер MeetUp.
func TestWebSecretsRulesAccount(t *testing.T) {
	c, h := newWebStand(t)
	w := request(h, "POST", "/api/settings", `{"room_password":"сезам-откройся","e2e_phrase":"тайная-фраза-123","obey_new":false}`, nil)
	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != 200 || !st.HasPassword || !st.HasPhrase || st.ObeyNew {
		t.Fatalf("секреты: %d %+v %v", w.Code, st, err)
	}
	if body := request(h, "GET", "/api/state", "", nil).Body.String(); strings.Contains(body, "сезам") || strings.Contains(body, "тайная") {
		t.Fatalf("состояние выдаёт секреты: %s", body)
	}
	w = request(h, "POST", "/api/settings", `{"room_password":"","e2e_phrase":""}`, nil)
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.HasPassword || st.HasPhrase {
		t.Fatalf("секреты не стёрты: %+v", st)
	}

	w = request(h, "POST", "/api/obey", `{"key":"h|n|анна","name":"Анна","allow":false}`, nil)
	st = State{}
	json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || len(st.Rules) != 1 || st.Rules[0].Allow || st.Rules[0].Name != "Анна" {
		t.Fatalf("правило: %d %+v", w.Code, st.Rules)
	}
	w = request(h, "POST", "/api/obey", `{"key":"h|n|анна","allow":null}`, nil)
	st = State{}
	json.Unmarshal(w.Body.Bytes(), &st)
	if len(st.Rules) != 0 {
		t.Fatalf("правило не забыто: %+v", st.Rules)
	}
	if w := request(h, "POST", "/api/obey", `{"allow":true}`, nil); w.Code != 400 {
		t.Fatalf("правило без участника: %d", w.Code)
	}

	// аккаунт: без ссылки некуда входить; с сервером — вход, сессия наружу не выдаётся
	if w := request(h, "POST", "/api/account", `{"login":"kolonka","password":"x"}`, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "ссылку") {
		t.Fatalf("вход без ссылки: %d %s", w.Code, w.Body)
	}
	srv := prototest.New(t)
	srv.AddAccount("kolonka", "secret-pass", "Станция")
	c.Update(func(s *Settings) { s.Link = srv.Link("room1") })
	if w := request(h, "POST", "/api/account", `{"login":"kolonka","password":"мимо"}`, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "неверный логин или пароль") {
		t.Fatalf("неверный пароль: %d %s", w.Code, w.Body)
	}
	w = request(h, "POST", "/api/account", `{"login":"kolonka","password":"secret-pass"}`, nil)
	st = State{}
	json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || !strings.HasPrefix(st.Account, "kolonka на ") || strings.Contains(w.Body.String(), "session-") || strings.Contains(w.Body.String(), "secret-pass") {
		t.Fatalf("вход: %d %s", w.Code, w.Body)
	}
	w = request(h, "POST", "/api/account", `{"logout":true}`, nil)
	st = State{}
	json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || st.Account != "" {
		t.Fatalf("выход: %d %+v", w.Code, st)
	}
}
