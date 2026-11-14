package proto

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Аккаунт MeetUp (§3.1–3.2): вход по логину и паролю даёт куку сессии meetup_session. Сервер проверяет её в рукопожатии
// WebSocket и привязывает соединение к аккаунту: имя берётся из профиля, идентификатор участника постоянный
// (0x80000000 + id аккаунта), у участника видна аватарка (§2.4).

const (
	sessionCookie = "meetup_session"
	// accountIDBase — с этого числа начинаются идентификаторы участников, вошедших под аккаунтом (§2.4).
	accountIDBase = 0x80000000
	apiTimeout    = 10 * time.Second
)

// Account — результат входа в аккаунт. Token — кука сессии: секрет, в журнал и в состояние не попадает.
type Account struct {
	Token  string
	UserID int64
	Login  string
	Name   string // отображаемое имя из профиля
}

// APIError — сервер ответил на запрос HTTP API ошибкой с машинным кодом (§3).
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	switch e.Code {
	case "wrong_credentials":
		return "неверный логин или пароль"
	case "no_session":
		return "сессия аккаунта истекла: войдите заново"
	case "":
		return fmt.Sprintf("сервер MeetUp ответил %d", e.Status)
	}
	return fmt.Sprintf("сервер MeetUp ответил «%s»", e.Code)
}

// BaseURL — адрес HTTP API сервера из ссылки на конференцию.
func (l Link) BaseURL() string {
	if l.Secure {
		return "https://" + l.Host
	}
	return "http://" + l.Host
}

// UserIDOf — id аккаунта участника по его идентификатору в комнате; 0 — участник вошёл без аккаунта.
func UserIDOf(senderID uint32) int64 {
	if senderID < accountIDBase {
		return 0
	}
	return int64(senderID - accountIDBase)
}

func apiClient(cfg *tls.Config) *http.Client {
	return &http.Client{Timeout: apiTimeout, Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // пароль и кука не уходят по перенаправлению
}

// apiCall шлёт запрос HTTP API и разбирает ответ; токен, если задан, идёт кукой сессии.
func apiCall(ctx context.Context, cfg *tls.Config, method, url, token string, body, out any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	resp, err := apiClient(cfg).Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с сервером MeetUp: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return resp, &APIError{Status: resp.StatusCode, Code: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp, errors.New("ответ сервера MeetUp не разобран")
		}
	}
	return resp, nil
}

// Login входит в аккаунт на сервере из ссылки l. Пароль уходит серверу (по https — под TLS) и нигде не сохраняется.
func Login(ctx context.Context, l Link, login, password string, cfg *tls.Config) (Account, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return Account{}, errors.New("нужны логин и пароль")
	}
	var out struct {
		User struct {
			ID          int64  `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	resp, err := apiCall(ctx, cfg, http.MethodPost, l.BaseURL()+"/api/auth/login", "", map[string]string{"login": login, "password": password}, &out)
	if err != nil {
		return Account{}, err
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie && ck.Value != "" {
			return Account{Token: ck.Value, UserID: out.User.ID, Login: out.User.Login, Name: out.User.DisplayName}, nil
		}
	}
	return Account{}, errors.New("сервер MeetUp принял пароль, но не выдал сессию")
}

// Logout завершает сессию аккаунта на сервере (кука перестаёт действовать).
func Logout(ctx context.Context, l Link, token string, cfg *tls.Config) error {
	_, err := apiCall(ctx, cfg, http.MethodPost, l.BaseURL()+"/api/auth/logout", token, nil, nil)
	return err
}
