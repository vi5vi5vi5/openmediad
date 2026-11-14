package meetup

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed page.html
var page []byte

// Страница настроек: одна встроенная страница без внешних файлов и три адреса API. Открыта компьютерам домашней сети без
// пароля — как и ssh колонки; от чужих веб-страниц в браузере хозяина защищают проверки в guard.
//
//	GET  /              страница
//	GET  /api/state     состояние (State): без ссылки и кода комнаты
//	POST /api/settings  изменить настройки: любое подмножество {link, name, connect, audio, chat, alice, volume,
//	                    room_password, e2e_phrase, obey_new}; пустая строка стирает пароль или фразу
//	POST /api/account   войти в аккаунт MeetUp: {login, password} (пароль уходит серверу MeetUp и не сохраняется);
//	                    выйти: {logout: true}
//	POST /api/obey      личное правило для участника: {key, name, allow}; allow: true | false | null (забыть правило)
//	POST /api/say       написать в чат комнаты: {text}
//
// Секреты (ссылка, пароль комнаты, фраза, сессия аккаунта) API принимает, но никогда не возвращает.
// Заглушка: пароля на саму страницу пока нет (README.md).

func (c *Component) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || !trustedHost(r.Host) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if !trustedHost(r.Host) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, c.Snapshot())
	})
	mux.HandleFunc("/api/settings", guard(func(w http.ResponseWriter, body []byte) {
		var in struct {
			Link    *string `json:"link"`
			Name    *string `json:"name"`
			Connect *bool   `json:"connect"`
			Audio   *bool   `json:"audio"`
			Chat    *bool   `json:"chat"`
			Alice   *bool   `json:"alice"`
			Volume  *int    `json:"volume"`
			Pass    *string `json:"room_password"`
			Phrase  *string `json:"e2e_phrase"`
			ObeyNew *bool   `json:"obey_new"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeError(w, http.StatusBadRequest, "запрос не разобран: нужен JSON")
			return
		}
		_, err := c.Update(func(s *Settings) {
			set(&s.Link, in.Link)
			set(&s.Name, in.Name)
			set(&s.Connect, in.Connect)
			set(&s.Audio, in.Audio)
			set(&s.Chat, in.Chat)
			set(&s.Alice, in.Alice)
			set(&s.Volume, in.Volume)
			set(&s.RoomPassword, in.Pass)
			set(&s.Phrase, in.Phrase)
			set(&s.ObeyNew, in.ObeyNew)
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c.Snapshot())
	}))
	mux.HandleFunc("/api/account", guard(func(w http.ResponseWriter, body []byte) {
		var in struct {
			Login    string `json:"login"`
			Password string `json:"password"`
			Logout   bool   `json:"logout"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeError(w, http.StatusBadRequest, "запрос не разобран: нужен JSON")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if in.Logout {
			c.Logout(ctx)
		} else if err := c.Login(ctx, in.Login, in.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c.Snapshot())
	}))
	mux.HandleFunc("/api/obey", guard(func(w http.ResponseWriter, body []byte) {
		var in struct {
			Key   string `json:"key"`
			Name  string `json:"name"`
			Allow *bool  `json:"allow"`
		}
		if json.Unmarshal(body, &in) != nil || in.Key == "" || len(in.Key) > 400 || len(in.Name) > 400 {
			writeError(w, http.StatusBadRequest, "нужен участник (key)")
			return
		}
		if _, err := c.Update(func(s *Settings) { s.setRule(in.Key, in.Name, in.Allow) }); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c.Snapshot())
	}))
	mux.HandleFunc("/api/say", guard(func(w http.ResponseWriter, body []byte) {
		var in struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(body, &in) != nil || strings.TrimSpace(in.Text) == "" {
			writeError(w, http.StatusBadRequest, "нужен непустой текст")
			return
		}
		if !c.say(strings.TrimSpace(in.Text)) {
			writeError(w, http.StatusConflict, "колонка сейчас не в комнате")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	return mux
}

func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// guard пропускает только запросы на изменение, которые могла послать сама страница настроек или программа, но не чужая
// веб-страница в браузере хозяина: метод POST, тело JSON (такой запрос с чужого сайта браузер без разрешения не отправит),
// заголовок Origin, если он есть, совпадает с адресом колонки, а сам адрес — не подставное доменное имя.
func guard(h func(w http.ResponseWriter, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "только POST")
			return
		}
		if !trustedHost(r.Host) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, http.StatusForbidden, "запрос отклонён")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				writeError(w, http.StatusForbidden, "запрос с чужой страницы отклонён")
				return
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		if err != nil {
			writeError(w, http.StatusBadRequest, "запрос не прочитан")
			return
		}
		h(w, body)
	}
}

// trustedHost — страницу открыли по адресу, который не может быть подставным доменом (атака с подменой DNS): IP-адрес,
// localhost, имя без точек или имя в домашней сети (*.local, *.lan, *.home).
func trustedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home", ".localhost"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// listen запускает страницу настроек на addr и работает, пока не отменён ctx.
func (c *Component) listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: c.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		srv.Close()
	}()
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}
